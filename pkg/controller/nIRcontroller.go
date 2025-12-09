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
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nodeIssueReportLister "xingzhan-node-autoreplace/pkg/generated/listers/nodeissuereport/v1alpha1"

	informercorev1 "k8s.io/client-go/informers/core/v1"

	"k8s.io/kubectl/pkg/drain"
	// policyv1beta1 "k8s.io/api/policy/v1beta1"
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
	nodelister            listercorev1.NodeLister
	selfpodname           string
	selfpodnamespace      string
	selfnodename          string
	logger                log.Entry
}

func init() {
	newNodeChan = make(chan string, 10)
}

func (n *NIRController) enqueue(obj interface{}) {
	eventkey, err := cache.MetaNamespaceKeyFunc(obj)

	if err != nil {
		n.logger.Println("Error getting key: ", err)
		return
	}
	n.queue.Add(eventkey)

}

func (n *NIRController) nIRAddFunctionHandler(obj interface{}) {
	n.enqueue(obj)
}

func (n *NIRController) nIRUpdateFunctionHandler(oldObj, newObj interface{}) {
	oldObjnIrR, err := json.Marshal(oldObj)
	if err != nil {
		n.logger.Errorln("failed to Marshal oldobj", err)
	}
	n.logger.Infoln("oldObjnIrR: ", string(oldObjnIrR))

	newObjnIrR, err := json.Marshal(newObj)
	if err != nil {
		n.logger.Errorln("failed to Marshal newobj", newObjnIrR)
	}
	n.logger.Infoln("newObjnIrR: ", string(newObjnIrR))

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
	// check if new node become ready, filt out old nodes become ready again
	isNewNodeBecomeReady := time.Since(newNodeObj.ObjectMeta.CreationTimestamp.Time) <= 15*time.Minute
	oldReady := n.isNodeReady(oldNodeObj)
	newReady := n.isNodeReady(newNodeObj)

	if !oldReady && newReady && isNewNodeBecomeReady {
		n.logger.Infoln("New node joined and ready:", newNodeObj.Name)
		select {
		case newNodeChan <- newNodeObj.Name:
			n.logger.Infoln("Sent node  to newNodeChan", newNodeObj.Name)
		default:
			n.logger.Warningf("newNodeChan blocked, failed to send %s", newNodeObj.Name)
		}
	}
}

// func (n *NIRController) cordonNode(nodename string) error {

// 	ctx := context.TODO()
// 	nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(ctx, nodename, metav1.GetOptions{})
// 	if err != nil {
// 		n.logger.Errorln("failed to get node when trying to cordon node", err)
// 		return err
// 	}
// 	if nodeobj.Spec.Unschedulable {
// 		n.logger.Infoln("Node  is already unschedulable", nodename)
// 		return nil
// 	}
// 	nodeobj.Spec.Unschedulable = true

// 	_, err = n.kubeclient.CoreV1().Nodes().Update(ctx, nodeobj, metav1.UpdateOptions{})
// 	if err != nil {
// 		n.logger.Errorln("failed to cordon node when trying to cordon update node", err)
// 		return err
// 	}
// 	return nil

// }

// func (n *NIRController) listPodOnNodes(nodename string) ([]v1.Pod, error) {
// 	ctx := context.TODO()

// 	podlist, err := n.kubeclient.CoreV1().Pods("").List(ctx, metav1.ListOptions{
// 		FieldSelector: "spec.nodeName=" + nodename,
// 	})
// 	if err != nil {
// 		//n.logger.Errorln("when drain node, failed to list pod on nodes", err)
// 		return nil, err
// 	}
// 	return podlist.Items, nil
// }

// func (n *NIRController) evictPod(pod v1.Pod) error {
// 	ctx := context.TODO()

// 	eviction := &policyv1beta1.Eviction{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      pod.Name,
// 			Namespace: pod.Namespace,
// 		},
// 	}

// 	err := n.kubeclient.CoreV1().Pods(pod.Namespace).Evict(ctx, eviction)
// 	if err != nil {
// 		//n.logger.Errorln("failed to evict pod when trying to evict pod", pod.Name, pod.Namespace, err)
// 		return err
// 	}
// 	return nil

// }

// func (n *NIRController) isDaemonset(pod v1.Pod) bool {
// 	ownerlist := pod.OwnerReferences
// 	for _, owner := range ownerlist {
// 		if owner.Kind == "DaemonSet" {
// 			return true
// 		}
// 	}
// 	return false
// }

// func (n *NIRController) isSelfPod(pod v1.Pod) bool {

// 	if pod.Name == n.selfpodname && pod.Namespace == n.selfpodnamespace && pod.Spec.NodeName == n.selfnodename {
// 		n.logger.Info("controller itself pod, skipped")
// 		return true
// 	}
// 	return false

// }

func (n *NIRController) drainNode(nodeobj v1.Node) error {

	//1. cordon node
	//2. list pods
	//3. evict none ds pods

	//1. cordon node
	// err := n.cordonNode(nodename)
	// if err != nil {
	// 	n.logger.Errorln("failed to drain node when trying to cordon node", err)
	// 	return err
	// }

	// //2. list pods
	// podlist, err := n.listPodOnNodes(nodename)
	// if err != nil {
	// 	n.logger.Errorln("when drain node, failed to list pod on nodes", err)
	// }

	// //3. evict none ds pods
	// for _, pod := range podlist {
	// 	//if pod.ObjectMeta.OwnerReferences != "DaemonSet" {
	// 	//	n.evictPod(pod)
	// 	//}
	// 	if !n.isDaemonset(pod) && !n.isSelfPod(pod) {
	// 		err := n.evictPod(pod)
	// 		if err != nil {
	// 			n.logger.Errorln("failed to evict pod when trying to evict pod", pod.Name, pod.Namespace, err)
	// 			return err
	// 		}
	// 	}
	// }
	drainer := &drain.Helper{
		Ctx:                 context.Background(),
		Client:              &n.kubeclient,
		Force:               true,
		GracePeriodSeconds:  -1,
		IgnoreAllDaemonSets: true,
		Timeout:             5 * time.Minute,
		Out:                 os.Stdout,
		ErrOut:              os.Stderr,
		DeleteEmptyDirData:  true,
	}

	if err := drain.RunCordonOrUncordon(drainer, &nodeobj, true); err != nil {
		n.logger.Errorln("failed to cordon node during drain operation", err)
		return err
	}

	if err := drain.RunNodeDrain(drainer, nodeobj.Name); err != nil {
		n.logger.Errorln("failed to drain node", err)
		return err
	}
	return nil

}

func (n *NIRController) processNextItem() bool {
	key, shutdown := n.queue.Get()
	if shutdown {
		return false
	}
	defer n.queue.Done(key)

	n.logger.Infoln("Processing object: ", key)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		n.logger.Errorln("fail to split the key:", key)
		n.queue.AddRateLimited(key)
		return true
	}

	nodeIssueReport, err := n.nodeIssueReportLister.NodeIssueReports(namespace).Get(name)

	if err != nil {
		// npd-node-replace component rely on NIR resource to do reboot or replace actions, if the NIR resource is missing, maybe some action have been done, just skip it
		n.logger.Errorln("failed to get nodeIssueReport resource, maybe some action have been done", err)
		// n.queue.AddRateLimited(key)
		return true
	}

	nodename := nodeIssueReport.Spec.NodeName

	nodeobj, err := n.nodelister.Get(nodename)

	if err != nil {
		n.logger.Errorln("failed to get node object resource, process next time", err)
		n.queue.AddRateLimited(key)
		return true
	}

	problems := nodeIssueReport.Spec.NodeProblems

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseDrained {
		if nodeIssueReport.Name == n.selfnodename {

			// Notify admin with SNS
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, false); err != nil {
				n.logger.Error("[node drained phase] failed to notify admin when replace node", err)
				n.queue.AddRateLimited(key)
				return true
			}
			// delete nodeIssueReport
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[node drained phase] faild to delete nodeIssueReport", nodeIssueReport.Name)
			} else {
				n.logger.Infoln("[node drained phase] replace Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
			}
			// delete issue node
			if nodeIssueReport.Name == n.selfnodename {

				if err := n.kubeclient.CoreV1().Nodes().Delete(context.TODO(), n.selfnodename, metav1.DeleteOptions{}); err != nil {
					n.logger.Errorln("[node drained phase] when trying to delete self node, error happened:", err)
				}
				if err := n.kubeclient.CoreV1().Pods(n.selfpodnamespace).Delete(context.TODO(), n.selfpodname, metav1.DeleteOptions{}); err != nil {
					n.logger.Errorln("[node drained phase] when trying to delete self pod:", n.selfpodnamespace, n.selfpodname, "failed with error:", err)

				} else {
					n.logger.Infoln("[node drained phase] deleted self pod when replace the issue node")
				}

			}
		} else {
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, false); err != nil {
				n.logger.Error("[node drained phase] failed to notify admin when replace node", err)
				n.queue.AddRateLimited(key)
				return true
			}
			if err := n.kubeclient.CoreV1().Nodes().Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[node drained phase] when trying to delete none-self node, error happened:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[node drained phase] faild to delete nodeIssueReport", nodeIssueReport.Name, "with error:", err)
			} else {
				n.logger.Infoln("[node drained phase] replace Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
			}
			// return true
		}
		return true
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNewJoined {
		// TODO drain
		// err = n.drainNode(nodename)
		nodeobj, err := n.nodelister.Get(nodename)
		if err != nil {
			n.logger.Errorln("[node newnodejoined phase] fail to get node object when trying to drain node:", nodename, "with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		err = n.drainNode(*nodeobj)
		if err != nil {
			n.logger.Errorln("[node newnodejoined phase] fail to drain node:", err)
			// TODO: when failed to drain node,  drain operation may be never happen again, because no new node will join, need to fix this
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node newnodejoined phase] successfully drained node")

		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseDrained
		if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Infoln("[node newnodejoined phase] faile to change phase to drained with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node newnodejoined phase] successfully change to drained phase")
		return true
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseDetached {


		n.logger.Infoln("[node detached phase] Waiting for new node joining .....")
		select {
		case newNodeName := <-newNodeChan:
			n.logger.Infoln("[node detached phase] New node ready:", newNodeName)

			// fix Bug: if manually scale up node group, new node join in cluster, this channel will receive the new node name, but this new node is not the replacing node we are looking for,  should skip it
			newnodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.TODO(), newNodeName, metav1.GetOptions{})
			if err != nil {
				n.logger.Errorln("[node detached phase] fail to get new node object when trying to drain node:", newNodeName, "with error:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			isNewNodeBecomeReadycheck := time.Since(newnodeobj.ObjectMeta.CreationTimestamp.Time) <= 15*time.Minute

			if !isNewNodeBecomeReadycheck {
				n.logger.Infoln("[node detached phase] the new node is not created recently, may be an old node become ready again, just skip it:", newNodeName)
				n.queue.AddRateLimited(key)
				return true
			}

			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNewJoined
			if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				n.logger.Infoln("[node detached phase] faile to change phase to newnodejoined with error:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			n.logger.Infoln("[node detached phase] detached phase passed, change phase to newnodejoined ")
			return true
		// changed timout time from 5 minutes to 15 minutes
		case <-time.After(15 * time.Minute):
			n.logger.Errorln("timed out waiting for new node to become ready")
			n.queue.AddRateLimited(key)
			return true
		}
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseReplace {
		n.logger.Infoln("[node replace phase] do phase replace action for node:", nodename)
		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			n.logger.Errorln("fail to get the node:", nodename, "thus failed to deal with node issues: ", err)
			n.queue.AddRateLimited(key)
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]
		n.logger.Infoln("[node replace phase] before do replace action , get instance Id:", instanceId)
		asgId, err := n.awsOperator.GetASGId(instanceId)
		if err != nil {
			n.logger.Errorln("[node replace phase] faile to find ASG name from instance tag, check if tag 'aws:autoscaling:groupName' exist:", instanceId)
			n.queue.AddRateLimited(key)
			return true
		}

		err = n.awsOperator.DetachInstance(asgId, instanceId)
		if err != nil {
			n.logger.Errorln("[node replace phase] fail to detach instance:", err)
			n.queue.AddRateLimited(key)
			return true
		}

		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseDetached
		if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Infoln("[node replace phase] faile to chandge phase to detached with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node replace phase] replace phase pass, change to detached phase")
		return true
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseRebooted {
		n.logger.Infoln("[node rebooted phase] node has been rebooted, checking node status", nodename)

		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			n.logger.Errorln("[node rebooted phase] fail to get the node:", nodename, "with error", err)
			n.queue.AddRateLimited(key)
			return true
		}
		if nodeobj.Spec.Taints != nil {
			n.logger.Infoln("[node rebooted phase] node still has taints, need to uncordon the node first:", nodename)
			drainer := &drain.Helper{
				Ctx:                 context.Background(),
				Client:              &n.kubeclient,
				Force:               true,
				GracePeriodSeconds:  -1,
				IgnoreAllDaemonSets: true,
				Timeout:             5 * time.Minute,
				Out:                 os.Stdout,
				ErrOut:              os.Stderr,
				DeleteEmptyDirData:  true,
			}

			if err := drain.RunCordonOrUncordon(drainer, nodeobj, false); err != nil {
				n.logger.Errorln("[node rebooted phase] failed to uncordon node during rebooted operation", err)
				n.queue.AddRateLimited(key)
				return true
			}
			n.logger.Infoln("[node rebooted phase] successfully uncordoned node:", nodename)
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Spec.NodeName, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[node rebooted phase] faild to delete nodeIssueReport", nodeIssueReport.Name)
				n.queue.AddRateLimited(key)
				return true
			} else {
				n.logger.Infoln("[node rebooted phase] reboot Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
			}
			return true
		}
		return true
	}

	if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseReboot {
		n.logger.Infoln("[node reboot phase] do phase reboot action for node:", nodename)
		nodeobj, err := n.kubeclient.CoreV1().Nodes().Get(context.Background(), nodename, metav1.GetOptions{})
		if err != nil {
			n.logger.Errorln("[node reboot phase] fail to get the node:", nodename, "thus failed to deal with node issues: ", err)
			n.queue.AddRateLimited(key)
			return true
		}
		providerIDslice := strings.Split(nodeobj.Spec.ProviderID, "/")

		instanceId := providerIDslice[len(providerIDslice)-1]

		n.logger.Infoln("[node reboot phase] before do reboot action , get instance Id:", instanceId)

		// Added logic to drain node before reboot node.
		err = n.drainNode(*nodeobj)
		if err != nil {
			n.logger.Errorln("[node reboot phase] fail to drain node:", err)
			// TODO: when failed to drain node,  drain operation may be never happen again, because no new node will join, need to fix this
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node reboot phase] before rebooting node, successfully drained node")

		n.logger.Infoln("[node reboot phase] do something with node, rebooting node:", nodename)
		err = n.awsOperator.RebootInstance(instanceId)
		if err != nil {
			n.logger.Errorln("[node reboot phase] fail to reboot instance:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node reboot phase] successfully rebooted node:", nodename)

		// TODO: add function to notice user what happened, for investigating root cause
		if err := n.awsOperator.SNSNotify(*nodeIssueReport, false); err != nil {
			n.logger.Error("[node reboot phase] failed to notify admin when reboot node", err)
		}
		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseRebooted

		if _ , err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Errorln("[node reboot phase] faile to change phase to rebooted with error:", err)
		}else {
			n.logger.Infoln("[node reboot phase] successfully change to rebooted phase")
		}

		// if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Spec.NodeName, metav1.DeleteOptions{}); err != nil {
		// 	n.logger.Errorln("[node reboot phase] faild to delete nodeIssueReport", nodeIssueReport.Name)
		// } else {
		// 	n.logger.Infoln("[node reboot phase] reboot Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
		// }
		return true
	}

	if nodeIssueReport.Spec.Action != nodeIssueReportv1alpha1.None {
		// TODO add logic to exclude node with label "npd-node-replace-disabled=true"
		nodelabelmap := nodeobj.GetLabels()
		if val, exists := nodelabelmap["npd-node-replace-disabled"]; exists {
			if val == "true" {
				n.logger.Infoln("node has label npd-node-replace-disabled=true, user don't want to auto replace/reboot this node, just skip it:", nodename)
				if err := n.awsOperator.SNSNotify(*nodeIssueReport, true); err != nil {
					n.logger.Error("[node problem detected, skip node] failed to notify admin when node has npd-node-replace-disabled=true label", err)
					n.queue.AddRateLimited(key)
					return true
				} else {
					n.logger.Infoln("[node problem detected, skip node] successfully notified admin that node has npd-node-replace-disabled=true label:", nodename)
				}
				if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
					n.logger.Errorln("[node problem detected, skip node] faild to delete nodeIssueReport", nodeIssueReport.Name, "with error:", err)
					n.queue.AddRateLimited(key)
					return true
				} else {
					n.logger.Infoln("[node problem detected, skip node] node has labeled npd-node-replace-disabled=true, deleted nodeIssueReport:", nodeIssueReport.Name)
				}
				return true
			}
		}

		n.logger.Infoln("problem on node is not tolerated by user, do something")
		if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Reboot && nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			//TODO aws reboot action logic
			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReboot
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				n.logger.Errorln("[node none phase]  failed to change the phase to phase reboot, with error:", err)
			}
			return true
		} else if nodeIssueReport.Spec.Action == nodeIssueReportv1alpha1.Replace && nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			//TODO aws replace node logic
			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReplace
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				n.logger.Errorln("[node none phase]  failed to change the phase to phase replace, with error:", err)
			}
			return true
		}

	}

	// travesal all node problems
	// thinking replace will handle more unbearable issues, if NIR is tagged with replace ation , instanstly perform , is tagged with reboot action, continue to travesal problems on the node
	for problemname, problem := range problems {
		// get the tolerance config for specific senario
		n.logger.Infoln("travesal all node problems, current problem", problemname)
		tolerancecount := n.toleranceConfig.ToleranceCollection[problemname]

		if tolerancecount.Times <= problem.Count {
			//nodeIssueReport.Spec.Action = true
			toleranceAction := tolerancecount.Action
			if toleranceAction == config.ActionReboot {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Reboot
				// nodeIssueReport.Spec.Phase = NodeissuereporterV1alpha1.pha
				if result, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					n.logger.Errorln("failed to update NodeIssueReport:", err)
				} else {
					if resultjson, err := json.Marshal(result); err != nil {
						n.logger.Errorln("failed to Marshal Action Update result", err)
					} else {
						n.logger.Debugln("Action Update result", string(resultjson))
					}
				}
				return true

			} else if toleranceAction == config.ActionReplace {
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Replace
				// n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{})
				if result, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					n.logger.Errorln("failed to update NodeIssueReport:", err)
				} else {
					if resultjson, err := json.Marshal(result); err != nil {
						n.logger.Errorln("failed to Marshal Action Update result", err)
					} else {
						n.logger.Debugln("Action Update result", string(resultjson))
					}
				}
				return true

			}

		}
	}

	return true
}

func (n *NIRController) worker() {
	n.logger.Infoln("Running NIRcontorller worker")
	for n.processNextItem() {

	}
}

func (n *NIRController) Run(stopch <-chan struct{}) {
	n.logger.Println("Worker is processing events...")
	//tolerance, err := config.LoadConfiguration()

	//if err != nil {
	//	n.logger.Fatal("failed to load tolerance configuration", err)
	//}

	for i := 0; i < workercount; i++ {
		go wait.Until(n.worker, time.Second, stopch)
	}

	<-stopch
	// TODO delete all the NodeIssueReport resources
	n.logger.Infoln("Shutting down NIRController")
	close(newNodeChan)

}

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer, nodeIssueReportClient nirclient.Clientset, kubeclient kubernetes.Clientset, awsOperator awspkg.AwsOperator, nodeInformer informercorev1.NodeInformer) *NIRController {
	tolerancecoll, err := config.LoadConfiguration()
	if err != nil {
		log.Fatal("[NIR controller]failed to load tolerance configuration", err)
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
		nodelister:              nodeInformer.Lister(),
		selfpodname:             os.Getenv("SELF_POD_NAME"),
		selfpodnamespace:        os.Getenv("SELF_POD_NAMESPACE"),
		selfnodename:            os.Getenv("SELF_NODE_NAME"),
		logger:                  *log.WithField("component", "NIR controller"),
	}

	// Here modified at 2025/11/12
	n.nodeIssueReportInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    n.nIRAddFunctionHandler,
			UpdateFunc: n.nIRUpdateFunctionHandler,
		})

	n.nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: n.nodeUpdateHandler,
	})
	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n

}
