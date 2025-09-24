package controller

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
	awspkg "xingzhan-node-autoreplace/pkg/aws"
	"xingzhan-node-autoreplace/pkg/config"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nodeIssueReportLister "xingzhan-node-autoreplace/pkg/generated/listers/nodeissuereport/v1alpha1"

	informercorev1 "k8s.io/client-go/informers/core/v1"

	//"k8s.io/kubectl/pkg/drain"
	policyv1beta1 "k8s.io/api/policy/v1beta1"
)

const workercount = 3

// var tolerance config.Tolerance

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
	selfpodname string
	selfpodnamespace string
	selfnodename string
}

func init() {
	newNodeChan = make(chan string, 10) 
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

func (n *NIRController) nIRUpdateFunctionHandler(oldObj, newObj interface{}){
	oldObjnIrR, err := json.Marshal(oldObj)
	if err != nil {
		log.Errorln("failed to Marshal oldobj", err)
	}
	log.Infoln("oldObjnIrR: ", string(oldObjnIrR))


	newObjnIrR, err := json.Marshal(newObj)
	if err != nil {
		log.Errorln("failed to Marshal newobj", newObjnIrR)
	}
	log.Infoln("newObjnIrR: ", string(newObjnIrR))

	n.enqueue(newObj)
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

	oldReady := n.isNodeReady(oldNodeObj)
	newReady := n.isNodeReady(newNodeObj)

	if !oldReady && newReady {
		log.Infoln("New node joined and ready:", newNodeObj.Name)
		select {
		case newNodeChan <- newNodeObj.Name:
			log.Infoln("Sent node  to newNodeChan", newNodeObj.Name)
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
		log.Infoln("Node  is already unschedulable", nodename)
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

func (n *NIRController) isSelfPod(pod v1.Pod) bool{

	if pod.Name == n.selfpodname && pod.Namespace == n.selfpodnamespace && pod.Spec.NodeName == n.selfnodename {
		log.Info("controller itself pod, skipped")
		return true
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
		if !n.isDaemonset(pod) && !n.isSelfPod(pod) {
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

	log.Infoln("Processing object: ", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Errorln("fail to split the key:", key)
		n.queue.AddRateLimited(key)
		return true
	}

	nodeIssueReport, err := n.nodeIssueReportLister.NodeIssueReports(namespace).Get(name)

	if err != nil {
		log.Errorln("failed to get nodeIssueReport resource, process next time", err)
		n.queue.AddRateLimited(key)
		return true
	}
	nodename := nodeIssueReport.Spec.NodeName
	problems := nodeIssueReport.Spec.NodeProblems


	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseDrained {
		if nodeIssueReport.Name == n.selfnodename {

			// Notify admin with SNS
			if err := n.awsOperator.SNSNotify(*nodeIssueReport); err != nil {
				log.Error("[node drained phase] failed to notify admin when replace node", err)
				n.queue.AddRateLimited(key)
				return true
			}
			// delete nodeIssueReport
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Spec.NodeName, metav1.DeleteOptions{}); err != nil {
				log.Errorln("[node drained phase] faild to delete nodeIssueReport", nodeIssueReport.Name)
			}else {
				log.Infoln("[node drained phase] replace Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
			}
			// delete issye node
			if nodeIssueReport.Name == n.selfnodename {

			if err := n.kubeclient.CoreV1().Nodes().Delete(context.TODO(), n.selfnodename, metav1.DeleteOptions{}); err != nil {
				log.Errorln("[node drained phase] when trying to delete self node, error happened:", err)
			}
			if err := n.kubeclient.CoreV1().Pods(n.selfpodnamespace).Delete(context.TODO(), n.selfpodname, metav1.DeleteOptions{}); err != nil {
				log.Errorln("[node drained phase] when trying to delete self pod:", n.selfpodnamespace, n.selfpodname,"failed with error:", err)

			}else {
				log.Infoln("[node drained phase] deleted self pod when replace the issue node")
			}
			
		}else {
			if err := n.kubeclient.CoreV1().Nodes().Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
				log.Errorln("[node drained phase] when trying to delete none-self node, error happened:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			// return true
		}
		return true
	}
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNewJoined {
		// TODO drain
		err = n.drainNode(nodename)
			if err != nil {
				log.Errorln("[node newnodejoined phase] fail to drain node:", err)
				// TODO: when failed to drain node,  drain operation may be never happen again, because no new node will join, need to fix this
				n.queue.AddRateLimited(key)
				return true
			}
		log.Infoln("[node newnodejoined phase] successfully drained node")

		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseDrained
		if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			log.Infoln("[node newnodejoined phase] faile to change phase to drained with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		log.Infoln("[node newnodejoined phase] successfully change to drained phase")
		return true
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseDetached {

		// newNodeName := ""

		select {
		case newNodeName := <-newNodeChan:
			log.Infoln("New node ready:", newNodeName)

			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNewJoined
			if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				log.Infoln("[node detached phase] faile to change phase to newnodejoined with error:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			log.Infoln("[node detached phase] detached phase passed, change phase to newnodejoined ")
			return true
		case <-time.After(5 * time.Minute):
			log.Errorln("timed out waiting for new node to become ready")
			n.queue.AddRateLimited(key)
			return true
		}
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseReplace {
		log.Infoln("[node replace phase] do phase replace action for node:", nodename)
		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			log.Errorln("fail to get the node:", nodename, "thus failed to deal with node issues: ", err)
			n.queue.AddRateLimited(key)
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]
		log.Infoln("[node replace phase] before do replace action , get instance Id:", instanceId)
		asgId, err := n.awsOperator.GetASGId(instanceId)
		if err != nil {
			log.Errorln("[node replace phase] faile to find ASG name from instance tag, check if tag 'aws:autoscaling:groupName' exist:", instanceId)
			n.queue.AddRateLimited(key)
			return true
		}

		err = n.awsOperator.DetachInstance(asgId, instanceId)
		if err != nil {
			log.Errorln("[node replace phase] fail to detach instance:", err)
			n.queue.AddRateLimited(key)
			return true
		}

		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseDetached
		if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			log.Infoln("[node replace phase] faile to chandge phase to detached with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		log.Infoln("[node replace phase] replace phase pass, change to detached phase")
		return true
	}


	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseReboot {
		log.Infoln("[node reboot phase] do phase reboot action for node:", nodename)
		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			log.Errorln("[node reboot phase] fail to get the node:", nodename, "thus failed to deal with node issues: ", err)
			n.queue.AddRateLimited(key)
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]

		log.Infoln("[node reboot phase] before do reboot action , get instance Id:", instanceId)
		log.Infoln("do something with node, rebooting node:", nodename)
		err = n.awsOperator.RebootInstance(instanceId)
		if err != nil {
			log.Errorln("fail to reboot instance:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		log.Infoln("successfully rebooted node:", nodename)

		// TODO: add function to notice user what happened, for investigating root cause
		if err := n.awsOperator.SNSNotify(*nodeIssueReport); err != nil {
			log.Error("failed to notify admin when reboot node", err)
		}
		if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Spec.NodeName, metav1.DeleteOptions{}); err != nil {
			log.Errorln("faild to delete nodeIssueReport", nodeIssueReport.Name)
		}else {
			log.Infoln("reboot Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
		}
		return true
	}

	if nodeIssueReport.Spec.Action != nodeIssueReportv1alpha1.None {

		log.Infoln("problem on node is not tolerated by user, do something")
		if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Reboot && nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			//TODO aws reboot action logic
			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReboot
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				log.Errorln("[node none phase]  failed to change the phase to phase reboot, with error:", err)
			}
			return true
		} else if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Replace && nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			//TODO aws replace node logic
			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReplace
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				log.Errorln("[node none phase]  failed to change the phase to phase replace, with error:", err)
			}
			return true
		}

	}

	// travesal all node problems
	for problemname, problem := range problems {
		// get the tolerance config for specific senario
		log.Infoln("travesal all node problems, current problem", problemname)
		tolerancecount := n.toleranceConfig.ToleranceCollection[problemname]

		if tolerancecount.Times <= problem.Count {
			//nodeIssueReport.Spec.Action = true
			toleranceAction := tolerancecount.Action
			if toleranceAction == config.ActionReboot {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Reboot
				// nodeIssueReport.Spec.Phase = NodeissuereporterV1alpha1.pha
				if result, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					log.Errorln("failed to update NodeIssueReport:", err)
				}else {
					if resultjson, err := json.Marshal(result); err != nil{
						log.Errorln("failed to Marshal Action Update result", err)
					}else {
						log.Debugln("Action Update result", string(resultjson))
					}
				}
				
				return true
			} else if toleranceAction == config.ActionReplace {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Replace
				// n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{})
				if result, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					log.Errorln("failed to update NodeIssueReport:", err)
				}else {
					if resultjson, err := json.Marshal(result); err != nil{
						log.Errorln("failed to Marshal Action Update result", err)
					}else {
						log.Debugln("Action Update result", string(resultjson))
					}
				}
				return true
			}

		}
		// return true
		
	}

	return true
}

func (n *NIRController) worker() {
	log.Infoln("Running NIRcontorller worker")
	for n.processNextItem() {

	}
}

func (n *NIRController) Run(stopch <-chan struct{}) {
	log.Println("Worker is processing events...")
	//tolerance, err := config.LoadConfiguration()

	//if err != nil {
	//	log.Fatal("failed to load tolerance configuration", err)
	//}

	for i := 0; i < workercount; i++ {
		go wait.Until(n.worker, time.Second, stopch)
	}

	<-stopch
	// TODO delete all the NodeIssueReport resources
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
		selfpodname: os.Getenv("SELF_POD_NAME"),
		selfpodnamespace: os.Getenv("SELF_POD_NAMESPACE"),
		selfnodename: os.Getenv("SELF_NODE_NAME"),
	}

	nodeIssueReportInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: n.nIRAddFunctionHandler,
			UpdateFunc: n.nIRUpdateFunctionHandler,
		})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: n.nodeUpdateHandler,
	})
	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n

}
