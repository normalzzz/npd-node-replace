package controller

import (
	// "encoding/json"
	"os"
	// "text/template"
	"time"

	"context"
	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nirinformer "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"
	nirlister "xingzhan-node-autoreplace/pkg/generated/listers/nodeissuereport/v1alpha1"

	// "github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	Informercorev1 "k8s.io/client-go/informers/core/v1"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	// "time"
)

const (
	nodeworkercount = 2
)

type NodeController struct {
	nodeInformer Informercorev1.NodeInformer
	nodeLister   listercorev1.NodeLister
	nirclient    nirclient.Clientset

	nodeIssueReportInformer nirinformer.NodeIssueReportInformer

	nodeIssueReportLister nirlister.NodeIssueReportLister

	queue      workqueue.TypedRateLimitingInterface[string]
	delayqueue workqueue.TypedDelayingInterface[string]
	logger     log.Entry
	gracetime  time.Duration
}

func (c *NodeController) requeueDelayAfter(key string, err error) {
    delay := c.gracetime / 2 

    if errors.IsConflict(err) {
        delay = 5 * time.Second 
    }

    c.logger.Warnf("Requeuing key %s after %v due to error: %v", key, delay, err)
    c.delayqueue.AddAfter(key, delay)
}

func (c *NodeController) constructNodeIssueReportForNode(nodename string) *nodeIssueReportv1alpha1.NodeIssueReport {
	nodeproblem := make(map[string]nodeIssueReportv1alpha1.ProblemRecord)

	return &nodeIssueReportv1alpha1.NodeIssueReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodename,
			Namespace: "default",
		},
		Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
			NodeName:     nodename,
			NodeProblems: nodeproblem,
			NodeStatus:   nodeIssueReportv1alpha1.NodeNotReadyStatus,
			Action:       nodeIssueReportv1alpha1.None,
			Phase:        nodeIssueReportv1alpha1.PhaseNone,
		},
	}
}

func (c *NodeController) updateNodeIssueReport(nir *nodeIssueReportv1alpha1.NodeIssueReport, nodeobj *corev1.Node) error {
	nodestatus, _:= c.checknodestatus(nodeobj)

	switch nodestatus {
	case corev1.ConditionUnknown:
		nir.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeUnknownStatus
	case corev1.ConditionFalse:
		nir.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeNotReadyStatus
	default:
		nir.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeReadyStatus
	}
	// nir.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeNotReadyStatus

	_, err := c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Update(context.Background(), nir, metav1.UpdateOptions{})
	if err != nil {
		c.logger.Errorln("[from NodeController]failed to update node issue report", err)
		return err
	}
	return nil
}

func (c *NodeController) checknodestatus(node *corev1.Node) (corev1.ConditionStatus, bool) {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status, true
		}
	}
	// return false
	return corev1.ConditionUnknown, false
}

func (c *NodeController) requeue(key string) {
	c.logger.Infoln("Requeuing key:", key)
	c.queue.AddRateLimited(key)
}

func (c *NodeController) enqueue(obj interface{}) {
	eventkey, err := cache.MetaNamespaceKeyFunc(obj)

	if err != nil {
		c.logger.Infoln("Error getting key: ", err)
		return
	}
	c.queue.Add(eventkey)
}

func (c *NodeController) enqueueDelay(obj interface{}) {
	eventkey, err := cache.MetaNamespaceKeyFunc(obj)

	if err != nil {
		c.logger.Infoln("Error getting key: ", err)
		return
	}
	c.delayqueue.AddAfter(eventkey, c.gracetime)
}

func (c *NodeController) checkIfKarpenterNode(node *corev1.Node) bool {
	for _, nodeowner := range node.OwnerReferences{
		if nodeowner.Kind == "NodeClaim" {
			c.logger.Infoln("Node", node.Name, "is managed by Karpenter, skip it")
			return false
		}
	}
	return true
}

func (c *NodeController) nodeUpdateHandler(oldObj, newObj interface{}) {
	

	newNode, ok := newObj.(*corev1.Node)
	if !ok {
		c.logger.Infoln("Error casting newObj to Node")
		return
	}
	oldNode, ok := oldObj.(*corev1.Node)
	if !ok {
		c.logger.Infoln("Error casting oldObj to Node")
		return
	}

	if !c.checkIfKarpenterNode(newObj.(*corev1.Node)){
		return
	}

	// if !c.isNodeReady(newNode) && c.isNodeReady(oldNode) {
	// 	c.logger.Infof("Node %s change to not ready status", newNode.Name)
	// 	c.enqueue(newNode)
	// }
	oldNodeStatus, oldFound := c.checknodestatus(oldNode)
	newNodeStatus, newFound := c.checknodestatus(newNode)

	if newFound && oldFound {
		if newNodeStatus != corev1.ConditionTrue && oldNodeStatus == corev1.ConditionTrue {
			c.logger.Infof("Node %s change to %s status", newNode.Name, newNodeStatus)
			c.enqueue(newNode)
		}
	}
}

func (c *NodeController) deplayqueueWorker() {
	c.logger.Infoln("Delay Queue worker started")

	for c.processNextItemDelayqueue() {

	}

}

func (c *NodeController) processNextItemDelayqueue() bool {
	key, shutdown := c.delayqueue.Get()
	if shutdown {
		return true
	}
	defer c.delayqueue.Done(key)
	c.logger.Infoln("Delayqueue processing key:", key)

	nodeobj, err := c.nodeLister.Get(key)
	if err != nil {
		if errors.IsNotFound(err) {
			c.logger.Infoln("Node", key, "not found, maybe deleted, no need to process further")
			if err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Delete(context.TODO(), key, metav1.DeleteOptions{}); err != nil {
				c.logger.Errorln("failed to delete node issue report after node recovered to ready status, need to delete node issue report manually", err)
				c.requeueDelayAfter(key, err)
				return true
			}
			return true
		}
		c.logger.Error("Failed to get node from lister:", err)
		c.requeueDelayAfter(key, err)
		return true
	}
	if status, _ := c.checknodestatus(nodeobj); status != corev1.ConditionTrue {
		c.logger.Warnln("Node", nodeobj.Name, "failed the second time status check, update the node issue report resource to perform node replace action", time.Now())
		nodeissuereport, err := c.nodeIssueReportLister.NodeIssueReports("default").Get(nodeobj.Name)
		if err != nil {
			if errors.IsNotFound(err) {
				c.logger.Infoln("no node issue report found for the node: ", nodeobj.Name, " maybe deleted meanwhile, no need to process further")
				return true
			}
			c.logger.Error("Failed to get node issue report from lister:", err)
			c.requeueDelayAfter(key, err)
			return true
		}
		// notice here
		if status, _ := c.checknodestatus(nodeobj); status == corev1.ConditionUnknown{
			c.logger.Infoln("Node", nodeobj.Name, "status is unknown, set force replace action directly")
			nodeissuereport.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeUnknownStatus 
		}

		nodeissuereport.Spec.Action = nodeIssueReportv1alpha1.Replace
		_, err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Update(context.Background(), nodeissuereport, metav1.UpdateOptions{})
		if err != nil {
			c.logger.Errorln("failed to update node issue report to set replace action", err)
			c.requeueDelayAfter(key, err)
			return true
		}

	} else {
		c.logger.Infoln("Node", nodeobj.Name, "recoverd to ready status now, remove the not ready mark from node issue report resource", time.Now())
		nodeissuereport, err := c.nodeIssueReportLister.NodeIssueReports("default").Get(nodeobj.Name)
		if err != nil {
			c.logger.Error("Failed to get node issue report from lister:", err)
			c.logger.Error("maybe node issue report resource already deleted (some action has been done), no need to process further")
			return true
		}

		if c.checkIfNodeIssueReportHaveIssueRecorded(nodeissuereport) {
			nodeissuereport.Spec.NodeStatus = nodeIssueReportv1alpha1.NodeReadyStatus
			// nodeissuereport.Spec.Action = nodeIssueReportv1alpha1.None
			_, err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Update(context.Background(), nodeissuereport, metav1.UpdateOptions{})
			if err != nil {
				c.logger.Errorln("failed to update node issue report to set ready status", err)
				c.requeueDelayAfter(key, err)
				return true
			}
		}else {
			if err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Delete(context.TODO(), nodeissuereport.Name, metav1.DeleteOptions{}); err != nil {
				c.logger.Errorln("failed to delete node issue report after node recovered to ready status, need to delete node issue report manually", err)
				c.requeueDelayAfter(key, err)
				return true
			}
			c.logger.Infoln("deleted node issue report resource for node", nodeobj.Name, "as node recovered to ready status and no issue recorded")
		}
		

	}
	return true
}

func (c *NodeController) checkIfNodeIssueReportHaveIssueRecorded(nodeissuereport *nodeIssueReportv1alpha1.NodeIssueReport) bool {
	if len(nodeissuereport.Spec.NodeProblems) == 0 {
		c.logger.Infoln("no node problems recorded in node issue report, safe to delete the resource")
		return false
	}
	c.logger.Infoln("node problems recorded in node issue report, just update the node status to ready")
	return true
}

func (c *NodeController) worker() {
	c.logger.Infoln("Worker started")
	// Add your worker logic here
	for c.processNextItem() {

	}
}

func (c *NodeController) processNextItem() bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)
	c.logger.Infoln("processing key:", key)
	c.logger.Infoln("node status being not ready detected, reconfirming after delay:", time.Now())

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		c.logger.Errorln("fail to split the key:", key)
		c.requeue(key)
		return true
	}
	// get the node object, if not found, maybe deleted, no need to process further
	nodeobj, err := c.nodeLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			c.logger.Infoln("Node", key, "not found, maybe deleted, no need to process further")
			return true
		}
		c.logger.Error("Failed to get node from lister:", err)
		c.requeue(key)
		return true
	}
	nodename := nodeobj.Name
	nodeissuereport, err := c.nodeIssueReportLister.NodeIssueReports("default").Get(nodename)
	c.logger.Infoln("for now, update the node issue report resource for node", nodename)

	if errors.IsNotFound(err) {
		c.logger.Infoln("no node issue report found for the node: ", nodename)
		nir := c.constructNodeIssueReportForNode(nodename)
		_, err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports("default").Create(context.Background(), nir, metav1.CreateOptions{})
		if err != nil {
			c.logger.Errorln("failed to create node issue report", err)
			c.requeue(key)
			return true
		}

		c.logger.Infoln("created node Issue Report object, for node", nodename, ", at ", time.Now(), ", because node not ready detected")
		c.enqueueDelay(nodeobj)
		return true
	}

	c.logger.Infoln("updating NodeIssueReport for node:", nodeobj.Name, ":", key, time.Now())

	err = c.updateNodeIssueReport(nodeissuereport, nodeobj)
	if err != nil {
		c.logger.Errorln("failed to update node issue report", err)
		c.requeue(key)
		return true
	}

	c.enqueueDelay(nodeobj)
	// namespace, name, err := cache.SplitMetaNamespaceKey(key)
	return true
}

func (c *NodeController) Run(stopCh <-chan struct{}) {
	c.logger.Infoln("Starting Node Controller")
	if !cache.WaitForCacheSync(stopCh, c.nodeInformer.Informer().HasSynced) {
		c.logger.Infoln("Timed out waiting for caches to sync")
		return
	}
	for i := 0; i < nodeworkercount; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
		go wait.Until(c.deplayqueueWorker, time.Second, stopCh)
	}

	<-stopCh
	c.logger.Infoln("Shutting down Node controller")
}

func NewNodeController(nodeInformer Informercorev1.NodeInformer, nirclient nirclient.Clientset, nodeIssueReportInformer nirinformer.NodeIssueReportInformer) *NodeController {

	node_doublecheck_gracetime := os.Getenv("NODE_DOULBE_CHECK_GRACE_TIME")
	doublecheck_gracetime := 3 * time.Minute
	if node_doublecheck_gracetime != "" {
		if t, err := time.ParseDuration(node_doublecheck_gracetime + "m"); err != nil {
			doublecheck_gracetime = 3 * time.Minute
			log.Warnln("[component:node controller]failed to parse NODE_DOULBE_CHECK_GRACE_TIME env var, use default 3 minutes", err)
		}else {
			doublecheck_gracetime = t
			log.Infoln("[component:node controller]set double check grace time to", doublecheck_gracetime)
		}
	}

	c := NodeController{
		nodeInformer:            nodeInformer,
		nodeLister:              nodeInformer.Lister(),
		nirclient:               nirclient,
		nodeIssueReportInformer: nodeIssueReportInformer,
		nodeIssueReportLister:   nodeIssueReportInformer.Lister(),
		queue:                   workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Second, 30*time.Second)),
		delayqueue:              workqueue.NewTypedDelayingQueue[string](),
		logger:                  *log.WithField("component", "node controller"),
		gracetime:				 doublecheck_gracetime,
	}

	// c.logger.

	c.nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.nodeUpdateHandler,
	})

	return &c

}
