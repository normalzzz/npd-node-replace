package controller

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
	awspkg "xingzhan-node-autoreplace/pkg/aws"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"

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

	toleranceConfigInformer nodeIssueReport.ToleranceConfigInformer
	toleranceConfigLister   nodeIssueReportLister.ToleranceConfigLister

	queue workqueue.TypedRateLimitingInterface[string]

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
		if n.ifNeedAddToChan(newNodeObj) {
			select {
			case newNodeChan <- newNodeObj.Name:
				n.logger.Infoln("Sent node  to newNodeChan", newNodeObj.Name)
			default:
				n.logger.Warningf("newNodeChan blocked, failed to send %s", newNodeObj.Name)
			}
		}

	}
}

func (n *NIRController) ifNeedAddToChan(newNodeObj *v1.Node) bool {
	// add logic to determin if the newready node is expected to add to channel:
	nodeIssueReportlist, err := n.nodeIssueReportLister.List(labels.Everything())
	if err != nil {
		n.logger.Errorln("failed to list NodeIssueReport resources when check if need add to chan:", err)
		return false
	}
	if nodeIssueReportlist == nil || len(nodeIssueReportlist) == 0 {
		n.logger.Infoln("no NodeIssueReport resources exist, no need to add new node to chan:", newNodeObj.Name)
		return false
	}
	for _, nodeIssueReport := range nodeIssueReportlist {
		if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseDetached {
			oldNodeObj, err := n.nodelister.Get(nodeIssueReport.Spec.NodeName)
			if err != nil {
				n.logger.Errorln("failed to get old node object when check if need add to chan:", err)
				continue
			}
			if !n.checkIfNewnodeExpected(oldNodeObj, newNodeObj) {
				n.logger.Infoln("the new node is not expected node for replacement, skip it:", newNodeObj.Name)
				continue
			}
			n.logger.Infoln("there is NodeIssueReport resource in detached phase, this new node is what we expect, need to add new node to chan:", newNodeObj.Name)
			return true
		}
	}
	return false
}

func (n *NIRController) taintNoexecuteOnNode(nodeobj *v1.Node) error {
	nodeobj.Spec.Taints = append(nodeobj.Spec.Taints, v1.Taint{
		Effect: v1.TaintEffectNoExecute,
		Key:    "npd-node-replace/taint",
		Value:  "unreachable",
	})
	_, err := n.kubeclient.CoreV1().Nodes().Update(context.TODO(), nodeobj, metav1.UpdateOptions{})
	if err != nil {
		n.logger.Errorln("failed to taint NoExecute on node:", nodeobj.Name, "with error:", err)
		return err
	}
	n.logger.Infoln("successfully tainted NoExecute on node:", nodeobj.Name)
	return nil

}

func (n *NIRController) drainNode(nodeobj v1.Node, forcely bool) error {
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

	if forcely {
		if err := drain.RunCordonOrUncordon(drainer, &nodeobj, true); err != nil {
			n.logger.Errorln("failed to cordon node during drain operation", err)
			return err
		}
		err := n.taintNoexecuteOnNode(&nodeobj)
		if err != nil {
			return err
		}
		// just try to drain the node once
		_ = drain.RunNodeDrain(drainer, nodeobj.Name)
		return nil

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
	if errors.IsNotFound(err) {
		n.logger.Infoln("node object not found, may be already deleted:", nodename)
		return true
	}
	if err != nil {
		n.logger.Errorln("failed to get node object resource, process next time", err)
		n.queue.AddRateLimited(key)
		return true
	}

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

		// for notready node, do normal drain

		// for unknown status node, do forcely drain
		if nodeIssueReport.Spec.NodeStatus == nodeIssueReportv1alpha1.NodeUnknownStatus {
			err = n.drainNode(*nodeobj, true)
			if err != nil {
				n.logger.Errorln("[node newnodejoined phase] fail to drain node forcely:", err)
				n.queue.AddRateLimited(key)
				return true
			}
		} else {
			err = n.drainNode(*nodeobj, false)
			if err != nil {
				n.logger.Errorln("[node newnodejoined phase] fail to drain node:", err)
				// TODO: when failed to drain node,  drain operation may be never happen again, because no new node will join, need to fix this
				n.queue.AddRateLimited(key)
				return true
			}
		}
		n.logger.Infoln("[node newnodejoined phase] successfully drained or deleted node")

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

		// Check if node is Ready - only proceed with uncordon after the node has
		// gone through NotReady (reboot actually happened) and come back to Ready.
		// We verify this by checking that enough time has passed since the reboot was initiated.
		rebootGracePeriod := 2 * time.Minute
		if !nodeIssueReport.Spec.LastActionTime.IsZero() && time.Since(nodeIssueReport.Spec.LastActionTime.Time) < rebootGracePeriod {
			n.logger.Infof("[node rebooted phase] waiting for reboot to take effect, %v since reboot, grace period %v", time.Since(nodeIssueReport.Spec.LastActionTime.Time).Round(time.Second), rebootGracePeriod)
			n.queue.AddRateLimited(key)
			return true
		}

		if !n.isNodeReady(nodeobj) {
			n.logger.Infoln("[node rebooted phase] node is not ready yet, waiting for it to recover:", nodename)
			n.queue.AddRateLimited(key)
			return true
		}

		// Node is Ready and grace period has passed - safe to uncordon
		n.logger.Infoln("[node rebooted phase] node is ready after reboot, uncordoning:", nodename)
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

		if err := n.awsOperator.SNSNotify(*nodeIssueReport, false); err != nil {
			n.logger.Error("[node rebooted phase] failed to notify admin after reboot completed", err)
		}

		if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Spec.NodeName, metav1.DeleteOptions{}); err != nil {
			n.logger.Errorln("[node rebooted phase] failed to delete nodeIssueReport", nodeIssueReport.Name)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node rebooted phase] reboot Action done, deleted nodeIssueReport:", nodeIssueReport.Name)
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
		err = n.drainNode(*nodeobj, false)
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

		if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Errorln("[node reboot phase] faile to change phase to rebooted with error:", err)
		} else {
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
		// Get the tolerance config for this node
		toleranceEntry, err := n.getToleranceConfigForNode(nodeobj)
		if err != nil {
			n.logger.Errorln("failed to get tolerance config for node:", nodename, "with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		if toleranceEntry == nil {
			n.logger.Infoln("no matching ToleranceConfig found for node:", nodename, ", skip action")
			return true
		}

		if !toleranceEntry.AllowOperation {
			n.logger.Infoln("ToleranceConfig.allowOperation is false for node:", nodename, ", only notify admin, skip action")
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, true); err != nil {
				n.logger.Error("[node problem detected, skip node] failed to notify admin", err)
				n.queue.AddRateLimited(key)
				return true
			}
			// the nodeissuereport resource could be kept,need do actions
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[node problem detected, skip node] failed to delete nodeIssueReport", nodeIssueReport.Name, "with error:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			n.logger.Infoln("[node problem detected, skip node] notified admin and deleted nodeIssueReport:", nodeIssueReport.Name)
			return true
		}

		if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			action := nodeIssueReportv1alpha1.Action(toleranceEntry.Action)
			if nodeIssueReport.Spec.Escalated {
				action = nodeIssueReportv1alpha1.Action(toleranceEntry.EscalateOperation)
				n.logger.Infoln("[escalation] escalating action to:", action, "for node:", nodename)
			}
			switch action {
			case nodeIssueReportv1alpha1.Reboot:
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReboot
			case nodeIssueReportv1alpha1.Replace:
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReplace
			case nodeIssueReportv1alpha1.Paging:
				n.logger.Infoln("[paging] action is paging, notify admin only for node:", nodename)
				if err := n.awsOperator.SNSNotify(*nodeIssueReport, true); err != nil {
					n.logger.Error("[paging] failed to notify admin", err)
					n.queue.AddRateLimited(key)
					return true
				}
				return true
			default:
				n.logger.Warnln("unknown action in ToleranceConfig:", action, "for node:", nodename)
				return true
			}
			nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				n.logger.Errorln("[node none phase] failed to change phase with error:", err)
			}
			return true
		}
		return true
	}

	// Score threshold check - scores are accumulated by EventController
	toleranceEntry, err := n.getToleranceConfigForNode(nodeobj)
	if err != nil {
		n.logger.Errorln("failed to get tolerance config for node:", nodename, "with error:", err)
		return true
	}
	if toleranceEntry == nil {
		n.logger.Infoln("no matching ToleranceConfig found for node:", nodename, ", skip problem evaluation")
		return true
	}

	if nodeIssueReport.Spec.ScoreInBucket >= toleranceEntry.BucketSize {
		// Check if this is an escalation: bucket overflowed again within cooldown window after a previous action
		isEscalation := false
		if !nodeIssueReport.Spec.LastActionTime.IsZero() && toleranceEntry.CooldownTimeInMinutes > 0 {
			cooldown := time.Duration(toleranceEntry.CooldownTimeInMinutes) * time.Minute
			if time.Since(nodeIssueReport.Spec.LastActionTime.Time) <= cooldown {
				isEscalation = true
			}
		}

		if isEscalation {
			n.logger.Infof("[escalation] score bucket overflow within cooldown for node %s: %d / %d, escalating to: %s",
				nodename, nodeIssueReport.Spec.ScoreInBucket, toleranceEntry.BucketSize, toleranceEntry.EscalateOperation)
			nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Action(toleranceEntry.EscalateOperation)
			nodeIssueReport.Spec.Escalated = true
		} else {
			n.logger.Infof("score bucket overflow for node %s: %d / %d, triggering action: %s",
				nodename, nodeIssueReport.Spec.ScoreInBucket, toleranceEntry.BucketSize, toleranceEntry.Action)
			nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Action(toleranceEntry.Action)
		}

		nodeIssueReport.Spec.ScoreInBucket = 0
		nodeIssueReport.Spec.LastActionTime = metav1.Now()
		nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
		if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.Background(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Errorln("failed to update NodeIssueReport after bucket overflow:", err)
		}
	}

	return true
}

// getToleranceConfigForNode finds the matching ToleranceConfigEntry for a given node by checking node labels.
func (n *NIRController) getToleranceConfigForNode(nodeobj *v1.Node) (*nodeIssueReportv1alpha1.ToleranceConfigEntry, error) {
	toleranceConfigs, err := n.toleranceConfigLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	if len(toleranceConfigs) == 0 {
		n.logger.Warnln("no ToleranceConfig resources found in cluster")
		return nil, nil
	}

	nodelabels := nodeobj.GetLabels()
	for _, tc := range toleranceConfigs {
		for i := range tc.Spec.Configs {
			entry := &tc.Spec.Configs[i]
			// nodeLabel format: "key=value"
			parts := strings.SplitN(entry.NodeLabel, "=", 2)
			if len(parts) != 2 {
				n.logger.Warnln("invalid nodeLabel format in ToleranceConfig, expected key=value, got:", entry.NodeLabel)
				continue
			}
			if val, exists := nodelabels[parts[0]]; exists && val == parts[1] {
				return entry, nil
			}
		}
	}
	return nil, nil
}

func (n *NIRController) checkIfNewnodeExpected(nodeobj *v1.Node, newnodeobj *v1.Node) bool {
	// add logic to determin if the newready node is exptected node for replacement
	isNewNodeBecomeReadycheck := time.Since(newnodeobj.ObjectMeta.CreationTimestamp.Time) <= 15*time.Minute

	if !isNewNodeBecomeReadycheck {
		n.logger.Infoln("[node detached phase] the new node is not created recently, may be an old node become ready again, just skip it:", newnodeobj.Name)
		return false
	}

	newnodelables := newnodeobj.GetLabels()
	oldnodelabels := nodeobj.GetLabels()
	if newnodelables["eks.amazonaws.com/nodegroup"] != oldnodelabels["eks.amazonaws.com/nodegroup"] {
		n.logger.Infoln("[node detached phase] the new node's nodegroup label is different from old node, is not the node we expepct", newnodeobj.Name)
		return false
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

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer, toleranceConfigInformer nodeIssueReport.ToleranceConfigInformer, nodeIssueReportClient nirclient.Clientset, kubeclient kubernetes.Clientset, awsOperator awspkg.AwsOperator, nodeInformer informercorev1.NodeInformer) *NIRController {
	n := &NIRController{
		nodeIssueReportInformer: nodeIssueReportInformer,
		toleranceConfigInformer: toleranceConfigInformer,
		toleranceConfigLister:   toleranceConfigInformer.Lister(),
		queue:                   workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Second, 30*time.Second)),
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
