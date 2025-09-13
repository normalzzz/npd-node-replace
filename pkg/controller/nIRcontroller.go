package controller

import (
	"context"
	log "github.com/sirupsen/logrus"
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

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nodeIssueReportLister "xingzhan-node-autoreplace/pkg/generated/listers/nodeIssueReport/v1alpha1"
)

const workercount = 3

var tolerance config.Tolerance

type NIRController struct {
	nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer

	queue workqueue.TypedRateLimitingInterface[string]
	// Add fields here for your controller's state, e.g. clientsets, informers, listers, etc.

	toleranceConfig config.ToleranceCollection

	nodeIssueReportLister nodeIssueReportLister.NodeIssueReportLister
	nodeIssueReportClient nirclient.Clientset
	kubeclient            kubernetes.Clientset
	awsOperator           awspkg.AwsOperator
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
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]
		log.Infoln("befor do operatoin, get instance Id:", instanceId)

		if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Reboot {
			//TODO aws reboot action logic
			log.Infoln("do something with node, rebooting node:", nodename)
			err = n.awsOperator.RebootInstance(instanceId)
			if err != nil {
				log.Errorln("fail to reboot instance:", err)
				return true
			}
			log.Infoln("successfully rebooted node:", nodename)

			return true
		} else if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Replace {
			//TODO aws replace node logic
			asgId, err := n.awsOperator.GetASGId(instanceId)
			if err != nil {
				log.Errorln("faile to find ASG name from instance tag, check if tag 'aws:autoscaling:groupName' exist:", instanceId)
				return true
			}

			err = n.awsOperator.DetachInstance(asgId, instanceId)
			if err != nil {
				log.Errorln("fail to detach instance:", err)
				return true
			}

			// TODO need to add logic to wait for new node join in, and then drain old node

			log.Infoln("found fatal errors, replaced node:", nodename)
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

}

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer, nodeIssueReportClient nirclient.Clientset, kubeclient kubernetes.Clientset, awsOperator awspkg.AwsOperator) *NIRController {
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
	}

	nodeIssueReportInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: n.nIRAddFunctionHandler,
		})
	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n

}
