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

package clusterprovisioner

import (
	eventingv1alpha1 "github.com/knative/eventing/pkg/apis/eventing/v1alpha1"
	"github.com/knative/eventing/pkg/buses/eventing/stub/channel"
	"github.com/knative/eventing/pkg/sidecar/multichannelfanout"
	"github.com/knative/eventing/pkg/sidecar/swappable"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sync"
)

const (
	// controllerAgentName is the string used by this controller to identify
	// itself when creating events.
	controllerAgentName = "stub-bus-cluster-provisioner-controller"
)

// ProvideController returns a flow controller.
func ProvideController(mgr manager.Manager, logger *zap.Logger) (controller.Controller, http.Handler, error) {
	logger = logger.With(zap.String("controller", controllerAgentName))

	h, err := swappable.NewEmptyHandler(logger)
	if err != nil {
		logger.Error("Unable to create HTTP handler", zap.Error(err))
		return nil, nil, err
	}

	// Setup a new controller to Reconcile ClusterProvisioners that are Stub buses.
	r :=  &reconciler{
		mgr: mgr,
		recorder: mgr.GetRecorder(controllerAgentName),
		logger: logger,
		swapHttpHandlerConfig: swapHttpHandlerConfig(h, sync.Mutex{}),
		channelControllers: make(map[corev1.ObjectReference]*channel.ConfigAndStopCh),
	}
	c, err := controller.New(controllerAgentName, mgr, controller.Options{
		Reconciler: r,
	})
	if err != nil {
		logger.Error("Unable to create controller.", zap.Error(err))
		return nil, nil, err
	}

	// Watch ClusterProvisioners.
	err = c.Watch(&source.Kind{
		Type: &eventingv1alpha1.ClusterProvisioner{},
	}, &handler.EnqueueRequestForObject{})
	if err != nil {
		logger.Error("Unable to watch ClusterProvisioners.", zap.Error(err), zap.Any("type", &eventingv1alpha1.ClusterProvisioner{}))
		return nil, nil, err
	}

	// TODO: Should we watch the K8s service as well? If it changes, we probably should change it
	// back.

	return c, h, nil
}

func swapHttpHandlerConfig(s *swappable.Handler, sLock sync.Mutex) func(multichannelfanout.Config) error {
	return func(config multichannelfanout.Config) error {
		sLock.Lock()
		defer sLock.Unlock()
		mch := s.GetMultiChannelFanoutHandler()
		if diff := mch.ConfigDiff(config); diff != "" {
			newH, err := mch.CopyWithNewConfig(config)
			if err != nil {
				return err
			}
			s.SetMultiChannelFanoutHandler(newH)
		}
		return nil
	}
}
