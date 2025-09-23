package controller

import (
	// "log"

	"encoding/json"
	"strings"
	"time"

	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	informercorev1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"context"
	nirinformer "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"
	nirlister "xingzhan-node-autoreplace/pkg/generated/listers/nodeissuereport/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	// nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/clientset/versioned/typed/nodeIssueReport/v1alpha1"
	// v1alpha1 "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
)

const (
	workerCount = 2
)

type EventController struct {
	// Add fields here for your controller's state, e.g. clientsets, informers, listers, etc.
	EventInformer informercorev1.EventInformer

	NodeLister listercorev1.NodeLister

	queue workqueue.TypedRateLimitingInterface[string]

	EventLister listercorev1.EventLister

	nodeIssueReportInformer nirinformer.NodeIssueReportInformer

	nodeIssueReportLister nirlister.NodeIssueReportLister

	kubeclient kubernetes.Clientset

	nirclient nirclient.Clientset

	controllerStartTime metav1.Time
}

func constructNodeIssueReport(event *corev1.Event) nodeIssueReportv1alpha1.NodeIssueReport {
	name := event.InvolvedObject.Name
	namespace := event.InvolvedObject.Namespace
	//nodeproblems := make(map[nodeIssueReportv1alpha1.ReasonRecord][]string)
	//nodeproblems[nodeIssueReportv1alpha1.ReasonRecord{Reason: event.Reason, Count: event.Count}] = []string{event.Message}
	//return nodeIssueReportv1alpha1.NodeIssueReport{
	//	ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	//	Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{NodeName: name, NodeProblems: nodeproblems},
	//}

	nodeprolems := make(map[string]nodeIssueReportv1alpha1.ProblemRecord)
	nodeprolems[event.Reason] = nodeIssueReportv1alpha1.ProblemRecord{
		Count:   1,
		Message: []string{event.Message},
	}

	return nodeIssueReportv1alpha1.NodeIssueReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
			NodeName:     event.InvolvedObject.Name,
			NodeProblems: nodeprolems,
			Action:       nodeIssueReportv1alpha1.None,
		},
	}
}

func (c *EventController) updateNodeIssueReport(nodeissuereport *nodeIssueReportv1alpha1.NodeIssueReport, event *corev1.Event) error {
	//nodeissuereport.Spec.NodeProblems[nodeIssueReportv1alpha1.ReasonRecord{Reason: event.Reason, Count: event.Count}] = []string{event.Message}
	//_, err := c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports(nodeissuereport.Namespace).Update(context.Background(), nodeissuereport, metav1.UpdateOptions{})
	//if err != nil {
	//	log.Errorln("failed to update node issue report", err)
	//	return err
	//}
	//return nil

	nodeprolems, exist := nodeissuereport.Spec.NodeProblems[event.Reason]
	if exist {
		nodeprolems.Count += 1
		nodeprolems.Message = append(nodeprolems.Message, event.Message)
		nodeissuereport.Spec.NodeProblems[event.Reason] = nodeprolems
	} else {

		nodeissuereport.Spec.NodeProblems[event.Reason] = nodeIssueReportv1alpha1.ProblemRecord{
			Count:   1,
			Message: []string{event.Message},
		}
	}
	_, err := c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports(nodeissuereport.Namespace).Update(context.Background(), nodeissuereport, metav1.UpdateOptions{})
	if err != nil {
		log.Errorln("failed to update node issue report", err)
		return err
	}
	return nil

}

func (c *EventController) processNextItem() bool {
	key, shutdown := c.queue.Get()
	if shutdown {
		return false
	}
	defer c.queue.Done(key)

	log.Infoln("Processing event: ", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Errorln("fail to split the key:", key)
		return true
	}
	// 1. get the event from the key
	event, err := c.EventLister.Events(namespace).Get(name)
	if err != nil {
		log.Errorln("failed to get event from key", err)
		return true
	}

	// json.Marshal(event)
	// log.Debugln("Event: ", )

	nodename := event.InvolvedObject.Name

	// usually the namespace is "default", inhirited from the events
	nodeissuereport, err := c.nodeIssueReportLister.NodeIssueReports(namespace).Get(nodename)
	// _ = nodeissuereport
	// 2. check if there is a node issue report for the node

	// 3. if there is no node issue report, create a new one
	// 4. if there is a node issue report, update the node issue report
	if errors.IsNotFound(err) {
		log.Infoln("no node issue report found for the node: ", nodename)
		nodeissuereport := constructNodeIssueReport(event)

		_, err = c.nirclient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Create(context.Background(), &nodeissuereport, metav1.CreateOptions{})
		if err != nil {
			log.Errorln("failed to create node issue report", err)
		}
		log.Infoln("constructed node Issue Report object")
		return true
	}

	err = c.updateNodeIssueReport(nodeissuereport, event)
	if err != nil {
		log.Errorln("failed to update node issue report", err)
		return true
	}

	// 3. if there is no node issue report, create a new one
	// 4. if there is a node issue report, update the node issue report

	// avoid duplicate event retrieval

	// TODO: handle the queued event key
	return true

}

func (c *EventController) worker() {
	// Implement your worker logic here, e.g. processing events from a queue.
	log.Println("Worker is processing events...")
	// Example: You can use c.EventInformer.Lister() to list events.

	for c.processNextItem() {

	}

}

func (c *EventController) Run(stopCh <-chan struct{}) {
	// Start your controller logic here, e.g. start informers, process events, etc.

	log.Println("Starting Event Controller")
	if !cache.WaitForCacheSync(stopCh, c.EventInformer.Informer().HasSynced) {
		log.Println("Timed out waiting for caches to sync")
		return
	}
	for i := 0; i < workerCount; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *EventController) enqueu(obj interface{}) {

	eventkey, err := cache.MetaNamespaceKeyFunc(obj)

	if err != nil {
		log.Errorln("failed to get event key", err)
		return
	}

	c.queue.Add(eventkey)

}

func (c *EventController) eventUpadteHandler(oldObj, newObj interface{}) {
	newevent, ok := newObj.(*corev1.Event)
	if !ok {
		log.Errorln("faile to get new event obj")
		return
	}

	if !c.isNodeProblemDetectorEvent(newevent) {
		return
	}

	if eventjson, err := json.Marshal(newevent); err != nil {
		log.Errorln("failed to marshal update new event to json", err)
	} else {
		log.Infoln("recieved update new events: \n", string(eventjson))
	}
	c.enqueu(newevent)
}

func (c *EventController) eventAddHandler(obj interface{}) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		log.Println("eventAddHandler: failed to cast object to Event")
		return
	}

	// only enqueue node-problem-detector node anomaly events
	if !c.isNodeProblemDetectorEvent(event) {
		return
	}

	// log.Infoln("New Event Captured: \n Kind: %s\n Name: %s\n Namespace: %s\n Reason: %s\n Message: %s\n",
	//     event.InvolvedObject.Kind,
	//     event.InvolvedObject.Name,
	//     event.InvolvedObject.Namespace,
	//     event.Reason,
	//     event.Message)

	if eventjson, err := json.Marshal(event); err != nil {
		log.Errorln("failed to marshal event to json", err)
	} else {
		log.Infoln("recieved whole events: \n", string(eventjson))
	}

	// enqued indicate that the node problem detector have detect the node problem
	// we will use this event to generate the node issue report(CRD)
	c.enqueu(event)

}

// isNodeProblemDetectorEvent checks whether the event comes from node-problem-detector
// (or its monitors) and targets a Node.
func (c *EventController) isNodeProblemDetectorEvent(e *corev1.Event) bool {
	if e == nil {
		return false
	}

	// Done added event filter based on time, ignore the events happened before controller started
	if e.LastTimestamp.Before(&c.controllerStartTime) {
		log.Infoln("event happened before controller start, ignored event", e.Name)
		return false
	}

	if e.InvolvedObject.Kind != "Node" {
		return false
	}

	// Done: check if node is handled by karpenter
	nodename := e.InvolvedObject.Name

	nodeobj, err := c.NodeLister.Get(nodename)
	if err != nil {
		log.Error("failed to get the node object when try to determain whether node is managed by karpenter")
		return false
	}
	for _, nodeowner := range nodeobj.OwnerReferences {
		if nodeowner.Kind == "NodeClaim" {
			log.Infoln("recieved node problem event, but node", nodename, "is handled by karpenter, thus ignore this event")
			return false
		}
	}

	component := e.Source.Component
	if component == "" {
		component = e.ReportingController
	}
	cl := strings.ToLower(component)

	sourcelist := []string{"kernel-monitor", "readonly-monitor", "network-custom-plugin-monitor", "iptables-mode-monitor", "health-checker", "docker-monitor", "disk-monitor", "ntp-custom-plugin-monitor", "abrt-adaptor"}

	for _, source := range sourcelist {
		if cl == source {
			return true
		}
	}

	for _, mf := range e.ManagedFields {
		if strings.Contains(strings.ToLower(mf.Manager), "node-problem-detector") {
			return true
		}
	}

	return false
}

func NewEventController(eventInformer informercorev1.EventInformer, nodeIssueReportInformer nirinformer.NodeIssueReportInformer, kubeclient kubernetes.Clientset, nirclient nirclient.Clientset, nodeInformer informercorev1.NodeInformer) *EventController {

	c := EventController{
		EventInformer:           eventInformer,
		NodeLister:              nodeInformer.Lister(),
		nodeIssueReportInformer: nodeIssueReportInformer,
		queue:                   workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Second, 30*time.Second)),
		EventLister:             eventInformer.Lister(),
		nodeIssueReportLister:   nodeIssueReportInformer.Lister(),
		kubeclient:              kubeclient,
		nirclient:               nirclient,
		controllerStartTime:     metav1.Time{Time: time.Now()},
	}
	// TODO add Fliter function , only pass the events concerning non-karpenter nodes and happened after eventcontroller started
	c.EventInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.eventAddHandler,
		UpdateFunc: c.eventUpadteHandler,
	})

	// c.EventInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
	// 	FilterFunc: ,
	// })
	// Add your event handlers here, e.g. for add, update, delete events.

	return &c
}
