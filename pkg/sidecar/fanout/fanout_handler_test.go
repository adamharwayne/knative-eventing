/*
Copyright 2018 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fanout

import (
	"errors"
	"fmt"
	"github.com/knative/eventing/pkg/buses"
	duckv1alpha1 "github.com/knative/pkg/apis/duck/v1alpha1"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Domains used in subscriptions, which will be replaced by the real domains of the started HTTP
// servers.
const (
	replaceCallable = "replaceCallable"
	replaceSinkable = "replaceSinkable"
)

var (
	cloudEventReq = httptest.NewRequest("POST", "http://channelname.channelnamespace/", body(cloudEvent))
	cloudEvent    = `{
    "cloudEventsVersion" : "0.1",
    "eventType" : "com.example.someevent",
    "eventTypeVersion" : "1.0",
    "source" : "/mycontext",
    "eventID" : "A234-1234-1234",
    "eventTime" : "2018-04-05T17:31:00Z",
    "extensions" : {
      "comExampleExtension" : "value"
    },
    "contentType" : "text/xml",
    "data" : "<much wow=\"xml\"/>"
}`
)

func TestFanoutHandler_ServeHTTP(t *testing.T) {
	testCases := map[string]struct {
		receiverFunc   func(buses.ChannelReference, *buses.Message) error
		timeout        time.Duration
		subs           []duckv1alpha1.ChannelSubscriberSpec
		callable       func(http.ResponseWriter, *http.Request)
		sinkable       func(http.ResponseWriter, *http.Request)
		expectedStatus int
	}{
		"rejected by receiver": {
			receiverFunc: func(buses.ChannelReference, *buses.Message) error {
				return errors.New("Rejected by test-receiver")
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"could not find tracked message": {
			receiverFunc: func(buses.ChannelReference, *buses.Message) error {
				// Not being written to messageStorage.
				return nil
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"fanout times out": {
			timeout: time.Millisecond,
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
				},
			},
			callable: func(writer http.ResponseWriter, _ *http.Request) {
				time.Sleep(10 * time.Millisecond)
				writer.WriteHeader(http.StatusOK)
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"zero subs succeed": {
			subs:           []duckv1alpha1.ChannelSubscriberSpec{},
			expectedStatus: http.StatusOK,
		},
		"empty sub succeeds": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{},
			},
			expectedStatus: http.StatusOK,
		},
		"sinkable fails": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					SinkableDomain: replaceSinkable,
				},
			},
			sinkable: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNotFound)
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"callable fails": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
				},
			},
			callable: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNotFound)
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"callable succeeds, sinkable fails": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
			},
			callable: callableSucceed,
			sinkable: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusForbidden)
			},
			expectedStatus: http.StatusInternalServerError,
		},
		"one sub succeeds": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
			},
			callable: callableSucceed,
			sinkable: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusAccepted)
			},
			expectedStatus: http.StatusOK,
		},
		"one sub succeeds, one sub fails": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
			},
			callable:       callableSucceed,
			sinkable:       (&succeedOnce{}).handler,
			expectedStatus: http.StatusInternalServerError,
		},
		"all subs succeed": {
			subs: []duckv1alpha1.ChannelSubscriberSpec{
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
				{
					CallableDomain: replaceCallable,
					SinkableDomain: replaceSinkable,
				},
			},
			callable: callableSucceed,
			sinkable: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusAccepted)
			},
			expectedStatus: http.StatusOK,
		},
	}
	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			callableServer := httptest.NewServer(&fakeHandler{
				handler: tc.callable,
			})
			defer callableServer.Close()
			sinkableServer := httptest.NewServer(&fakeHandler{
				handler: tc.sinkable,
			})
			defer sinkableServer.Close()

			// Rewrite the subs to use the servers we just started.
			subs := make([]duckv1alpha1.ChannelSubscriberSpec, 0)
			for _, sub := range tc.subs {
				if sub.CallableDomain == replaceCallable {
					sub.CallableDomain = callableServer.URL[7:] // strip the leading 'http://'
				}
				if sub.SinkableDomain == replaceSinkable {
					sub.SinkableDomain = sinkableServer.URL[7:] // strip the leading 'http://'
				}
				subs = append(subs, sub)
			}

			h := NewHandler(zap.NewNop(), Config{Subscriptions: subs}).(*fanoutHandler)
			if tc.receiverFunc != nil {
				h.receiver = buses.NewMessageReceiver(tc.receiverFunc, zap.NewNop().Sugar())
			}
			if tc.timeout != 0 {
				h.timeout = tc.timeout
			}

			w := httptest.NewRecorder()
			fmt.Sprintf("hello %v", n)
			h.ServeHTTP(w, cloudEventReq)
			if w.Code != tc.expectedStatus {
				t.Errorf("Unexpected status code. Expected %v, Actual %v", tc.expectedStatus, w.Code)
			}
		})
	}
}

type fakeHandler struct {
	handler func(http.ResponseWriter, *http.Request)
}

func (h *fakeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body.Close()
	h.handler(w, r)
}

type succeedOnce struct {
	called atomic.Bool
}

func (s *succeedOnce) handler(w http.ResponseWriter, _ *http.Request) {
	if s.called.CAS(false, true) {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusForbidden)
	}
}

func body(body string) io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(body))
}
func callableSucceed(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte(cloudEvent))
}
