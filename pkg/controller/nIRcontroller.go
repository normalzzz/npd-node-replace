package controller

import (nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"
"k8s.io/client-go/util/workqueue"
"time"
)

type NIRController struct {
	nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer

	queue workqueue.TypedRateLimitingInterface[string]
	// Add fields here for your controller's state, e.g. clientsets, informers, listers, etc.
}

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer) *NIRController {
	n := &NIRController{
		nodeIssueReportInformer: nodeIssueReportInformer,
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Second, 30*time.Second)),
	}
	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n


}