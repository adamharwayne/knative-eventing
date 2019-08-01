package provisioners

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
)

// ServiceOption can be used to optionally modify the K8s default that gets created for the Dispatcher in CreateDispatcherService
type ServiceOption func(*v1.Service) error

func DispatcherLabels(ccpName string) map[string]string {
	return map[string]string{
		"clusterChannelProvisioner": ccpName,
		"role":                      "dispatcher",
	}
}

func channelDispatcherServiceName(ccpName string) string {
	return fmt.Sprintf("%s-dispatcher", ccpName)
}
