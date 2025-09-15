package controller

import (
	"context"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"strings"
	"time"
	awspkg "xingzhan-node-autoreplace/pkg/aws"
	"xingzhan-node-autoreplace/pkg/config"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"

	informercorev1 "k8s.io/client-go/informers/core/v1"
	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nodeIssueReportLister "xingzhan-node-autoreplace/pkg/generated/listers/nodeIssueReport/v1alpha1"
	//"k8s.io/kubectl/pkg/drain"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
)

const workercount = 3

var tolerance config.Tolerance

var newNodeChan chan string

type NIRController struct {
	nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer

	queue workqueue.TypedRateLimitingInterface[string]
	// Add fields here for your controller's state, e.g. clientsets, informers, listers, etc.

	toleranceConfig config.ToleranceCollection

	nodeIssueReportLister nodeIssueReportLister.NodeIssueReportLister
	nodeIssueReportClient nirclient.Clientset
	kubeclient            kubernetes.Clientset
	awsOperator           awspkg.AwsOperator
	nodeInformer          informercorev1.NodeInformer
}

func init() {
	newNodeChan = make(chan string, 10) // 带缓冲 channel，防止阻塞
}

func (n *NIRController) enqueue(obj interface{}) {
	eventkey, err := cache.MetaNamespaceKeyFunc(obj)

	if err != nil {
		log.Println("Error getting key: ", err)
		return
	}
	n.queue.Add(eventkey)

}

func (n *NIRController) nIRAddFunctionHandler(obj interface{}) {
	n.enqueue(obj)
}

func (n *NIRController) isNodeReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady {
			return condition.Status == v1.ConditionTrue
		}
	}
	return false
}

func (n *NIRController) nodeUpdateHandler(oldObj interface{}, newObj interface{}) {

	oldNodeObj := oldObj.(*v1.Node)

	newNodeObj := newObj.(*v1.Node)

	//oldNodeCondition := oldNodeObj.Status.Conditions
	//
	//newNodeConfition := newNodeObj.Status.Conditions
	//
	//for _, newcondition := range newNodeConfition {
	//
	//	if newcondition.Type == "Ready" && newcondition.Status == "True" {
	//		for _, oldcondition := range oldNodeCondition {
	//			if oldcondition.Type == "Ready" && oldcondition.Status == "False" {
	//				log.Infoln("new node join in:", newNodeObj.Name)
	//				newNodeChan <- newNodeObj.Name
	//
	//			}
	//
	//		}
	//	}
	//
	//}

	oldReady := n.isNodeReady(oldNodeObj)
	newReady := n.isNodeReady(newNodeObj)

	if !oldReady && newReady {
		log.Infoln("New node joined and ready:", newNodeObj.Name)
		select {
		case newNodeChan <- newNodeObj.Name:
			log.Infoln("Sent node %s to newNodeChan", newNodeObj.Name)
		default:
			log.Warningf("newNodeChan blocked, failed to send %s", newNodeObj.Name)
		}
	}
}

func (n *NIRController) cordonNode(nodename string) error {

	ctx := context.TODO()
	nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(ctx, nodename, metav1.GetOptions{})
	if err != nil {
		log.Errorln("failed to get node when trying to cordon node", err)
		return err
	}
	if nodeobj.Spec.Unschedulable {
		log.Infoln("Node %s is already unschedulable", nodename)
		return nil
	}
	nodeobj.Spec.Unschedulable = true

	_, err = n.kubeclient.CoreV1().Nodes().Update(ctx, nodeobj, metav1.UpdateOptions{})
	if err != nil {
		log.Errorln("failed to cordon node when trying to cordon update node", err)
		return err
	}
	return nil

}

func (n *NIRController) listPodOnNodes(nodename string) ([]v1.Pod, error) {
	ctx := context.TODO()

	podlist, err := n.kubeclient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodename,
	})
	if err != nil {
		//log.Errorln("when drain node, failed to list pod on nodes", err)
		return nil, err
	}
	return podlist.Items, nil
}

func (n *NIRController) evictPod(pod v1.Pod) error {
	ctx := context.TODO()

	eviction := &policyv1beta1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}

	err := n.kubeclient.CoreV1().Pods(pod.Namespace).Evict(ctx, eviction)
	if err != nil {
		//log.Errorln("failed to evict pod when trying to evict pod", pod.Name, pod.Namespace, err)
		return err
	}
	return nil

}

func (n *NIRController) isDaemonset(pod v1.Pod) bool {
	ownerlist := pod.OwnerReferences
	for _, owner := range ownerlist {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}
func (n *NIRController) drainNode(nodename string) error {

	//1. cordon node
	//2. list pods
	//3. evict none ds pods

	//1. cordon node
	err := n.cordonNode(nodename)
	if err != nil {
		log.Errorln("failed to drain node when trying to cordon node", err)
		return err
	}

	//2. list pods
	podlist, err := n.listPodOnNodes(nodename)
	if err != nil {
		log.Errorln("when drain node, failed to list pod on nodes", err)
	}

	//3. evict none ds pods
	for _, pod := range podlist {
		//if pod.ObjectMeta.OwnerReferences != "DaemonSet" {
		//	n.evictPod(pod)
		//}
		if !n.isDaemonset(pod) {
			err := n.evictPod(pod)
			if err != nil {
				log.Errorln("failed to evict pod when trying to evict pod", pod.Name, pod.Namespace, err)
				return err
			}
		}
	}

	return nil

}

func (n *NIRController) processNextItem() bool {
	key, shutdown := n.queue.Get()
	if shutdown {
		return false
	}
	defer n.queue.Done(key)

	log.Infoln("Processing event: ", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Errorln("fail to split the key:", key)
		n.queue.AddRateLimited(key)
		return true
	}

	nodeIssueReport, err := n.nodeIssueReportLister.NodeIssueReports(namespace).Get(name)

	nodename := nodeIssueReport.Spec.NodeName
	problems := nodeIssueReport.Spec.NodeProblems

	if nodeIssueReport.Spec.Action != nodeIssueReportv1alpha1.None {
		//action := n.toleranceConfig.ToleranceCollection[]

		log.Infoln("problem on node is not tolerated by user, do something")
		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			log.Errorln("fail to get the node:", nodename, "thus failed to deal with node issues: ", err)
			n.queue.AddRateLimited(key)
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]
		log.Infoln("before do operation, get instance Id:", instanceId)

		if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Reboot {
			//TODO aws reboot action logic
			log.Infoln("do something with node, rebooting node:", nodename)
			err = n.awsOperator.RebootInstance(instanceId)
			if err != nil {
				log.Errorln("fail to reboot instance:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			log.Infoln("successfully rebooted node:", nodename)

			return true
		} else if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Replace {
			//TODO aws replace node logic
			asgId, err := n.awsOperator.GetASGId(instanceId)
			if err != nil {
				log.Errorln("faile to find ASG name from instance tag, check if tag 'aws:autoscaling:groupName' exist:", instanceId)
				n.queue.AddRateLimited(key)
				return true
			}

			err = n.awsOperator.DetachInstance(asgId, instanceId)
			if err != nil {
				log.Errorln("fail to detach instance:", err)
				n.queue.AddRateLimited(key)
				return true
			}

			newNodeName := <-newNodeChan
			log.Infoln("New node ready:", newNodeName)
			// TODO need to add logic to wait for new node join in, and then drain old node

			err = n.drainNode(nodename)
			if err != nil {
				log.Errorln("fail to drain node:", err)
				n.queue.AddRateLimited(key)
				return true
			}

			log.Infoln("found fatal errors, replaced node:", nodename, "new node name:", newNodeName)
			return true
		}

	}

	// travesal all node problems
	for problemname, problem := range problems {
		// get the tolerance config for specific senario
		tolerancecount := n.toleranceConfig.ToleranceCollection[problemname]

		if tolerancecount.Times <= problem.Count {
			//nodeIssueReport.Spec.Action = true
			toleranceAction := tolerancecount.Action
			if toleranceAction == config.ActionReboot {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Reboot
				n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{})
				return true
			} else if toleranceAction == config.ActionReplace {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Replace
				n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{})
				return true
			}

		}
		return true
	}

	return true
}

func (n *NIRController) worker() {

	for n.processNextItem() {

	}
}

func (n *NIRController) Run(stopch <-chan struct{}) {
	log.Println("Worker is processing events...")
	//tolerance, err := config.LoadConfiguration()

	//if err != nil {
	//	log.Fatal("failed to load tolerance configuration", err)
	//}

	for i := 0; i < workerCount; i++ {
		go wait.Until(n.worker, time.Second, stopch)
	}

	<-stopch
	log.Infoln("Shutting down NIRController")
	close(newNodeChan)

}

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer, nodeIssueReportClient nirclient.Clientset, kubeclient kubernetes.Clientset, awsOperator awspkg.AwsOperator, nodeInformer informercorev1.NodeInformer) *NIRController {
	tolerancecoll, err := config.LoadConfiguration()
	if err != nil {
		log.Fatal("failed to load tolerance configuration", err)
	}
	n := &NIRController{
		nodeIssueReportInformer: nodeIssueReportInformer,
		queue:                   workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Second, 30*time.Second)),
		toleranceConfig:         tolerancecoll,
		nodeIssueReportLister:   nodeIssueReportInformer.Lister(),
		nodeIssueReportClient:   nodeIssueReportClient,
		kubeclient:              kubeclient,
		awsOperator:             awsOperator,
		nodeInformer:            nodeInformer,
	}

	nodeIssueReportInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: n.nIRAddFunctionHandler,
		})
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: n.nodeUpdateHandler,
	})
	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n

}
