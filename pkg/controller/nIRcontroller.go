package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	nirclient "xingzhan-node-autoreplace/pkg/generated/clientset/versioned"
	nodeIssueReport "xingzhan-node-autoreplace/pkg/generated/informers/externalversions/nodeIssueReport/v1alpha1"
	"xingzhan-node-autoreplace/pkg/metrics"

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

const (
	workercount = 3

	rebootStartedNotificationAnnotation   = "nodeissuereporter.xingzhan.io/reboot-started-notification"
	rebootCompletedNotificationAnnotation = "nodeissuereporter.xingzhan.io/reboot-completed-notification"
	notificationSending                   = "sending"
	notificationSent                      = "sent"
	notificationFailed                    = "failed"
)

type AWSOperations interface {
	RebootInstance(instanceID string) error
	DetachInstance(asgID string, instanceID string) error
	GetASGId(instanceID string) (string, error)
	SNSNotify(nodeIssueReportv1alpha1.NodeIssueReport, string) error
}

type NIRController struct {
	nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer

	toleranceConfigInformer nodeIssueReport.ToleranceConfigInformer
	toleranceConfigLister   nodeIssueReportLister.ToleranceConfigLister

	queue workqueue.TypedRateLimitingInterface[string]

	nodeIssueReportLister nodeIssueReportLister.NodeIssueReportLister
	nodeIssueReportClient nirclient.Interface
	kubeclient            kubernetes.Clientset
	awsOperator           AWSOperations
	nodeInformer          informercorev1.NodeInformer
	nodelister            listercorev1.NodeLister
	selfpodname           string
	selfpodnamespace      string
	selfnodename          string
	logger                log.Entry
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

// findReadyReplacementNode scans all nodes to find one that:
// 1. Was created recently (within 15 minutes)
// 2. Is Ready
// 3. Belongs to the same nodegroup as the old node
// Returns the node name if found, empty string otherwise.
func (n *NIRController) findReadyReplacementNode(oldNode *v1.Node) string {
	nodes, err := n.nodelister.List(labels.Everything())
	if err != nil {
		n.logger.Errorln("[node detached phase] failed to list nodes:", err)
		return ""
	}

	oldNodegroup := oldNode.GetLabels()["eks.amazonaws.com/nodegroup"]

	for _, node := range nodes {
		// Skip the old node itself
		if node.Name == oldNode.Name {
			continue
		}
		// Must be created recently (within 15 minutes)
		if time.Since(node.ObjectMeta.CreationTimestamp.Time) > 15*time.Minute {
			continue
		}
		// Must be Ready
		if !n.isNodeReady(node) {
			continue
		}
		// Must belong to the same nodegroup
		if node.GetLabels()["eks.amazonaws.com/nodegroup"] != oldNodegroup {
			continue
		}
		return node.Name
	}
	return ""
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

func (n *NIRController) drainNode(nodeobj *v1.Node, forcely bool) error {
	// Objects returned by informer listers are shared and must be treated as read-only.
	nodeobj = nodeobj.DeepCopy()
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
		if err := drain.RunCordonOrUncordon(drainer, nodeobj, true); err != nil {
			n.logger.Errorln("failed to cordon node during drain operation", err)
			return err
		}
		err := n.taintNoexecuteOnNode(nodeobj)
		if err != nil {
			return err
		}
		// just try to drain the node once
		_ = drain.RunNodeDrain(drainer, nodeobj.Name)
		return nil

	}

	if err := drain.RunCordonOrUncordon(drainer, nodeobj, true); err != nil {
		n.logger.Errorln("failed to cordon node during drain operation", err)
		return err
	}

	if err := drain.RunNodeDrain(drainer, nodeobj.Name); err != nil {
		n.logger.Errorln("failed to drain node", err)
		return err
	}
	return nil

}

// countActiveActionsForEntry counts how many NIRs matching the given tolerance entry
// are currently in an active phase (not PhaseNone).
func (n *NIRController) countActiveActionsForEntry(entry *nodeIssueReportv1alpha1.ToleranceConfigEntry) int32 {
	nirList, err := n.nodeIssueReportLister.List(labels.Everything())
	if err != nil {
		return 0
	}
	parts := strings.SplitN(entry.NodeLabel, "=", 2)
	if len(parts) != 2 {
		return 0
	}
	var count int32
	for _, nir := range nirList {
		if nir.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			continue
		}
		nodeobj, err := n.nodelister.Get(nir.Spec.NodeName)
		if err != nil {
			continue
		}
		if val, exists := nodeobj.GetLabels()[parts[0]]; exists && val == parts[1] {
			count++
		}
	}
	return count
}

// isDryRun checks if the tolerance entry has dry-run mode enabled.
// In dry-run mode, sends an SNS notification about what would happen but takes no action.
func (n *NIRController) isDryRun(entry *nodeIssueReportv1alpha1.ToleranceConfigEntry, nir *nodeIssueReportv1alpha1.NodeIssueReport, action string) bool {
	if !entry.DryRun {
		return false
	}
	reason := fmt.Sprintf("dry-run-%s", action)
	n.logger.Infof("[dry-run] would execute %s on node %s, but dry-run is enabled", action, nir.Spec.NodeName)
	if err := n.awsOperator.SNSNotify(*nir, reason); err != nil {
		n.logger.Errorf("[dry-run] failed to send dry-run notification: %v", err)
	}
	return true
}

func rebootNotificationActionID(nir *nodeIssueReportv1alpha1.NodeIssueReport) string {
	if !nir.Spec.LastActionTime.IsZero() {
		return nir.Spec.LastActionTime.Time.UTC().Format(time.RFC3339Nano)
	}
	if nir.UID != "" {
		return "uid:" + string(nir.UID)
	}
	return nir.Namespace + "/" + nir.Name
}

func notificationAnnotationValue(state, actionID string) string {
	return state + ":" + actionID
}

// used to update object with retry on conflict error, to avoid stale informer snapshot update
func retryOnConflict(update func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = update()
		if !errors.IsConflict(err) {
			return err
		}
		time.Sleep(time.Duration(1<<attempt) * 10 * time.Millisecond)
	}
	return err
}

// notifyRebootOnce claims a notification in NIR metadata before publishing it.
// Metadata annotations are used so this remains compatible with the existing CRD.
// A fresh API read plus resourceVersion-protected Update prevents a stale informer
// snapshot from publishing the same notification again.
// 使用 uuid 或者 LastActionTime 来标识一个 reboot action，防止 stale informer snapshot 重复发送通知
func (n *NIRController) notifyRebootOnce(
	ctx context.Context,
	nir *nodeIssueReportv1alpha1.NodeIssueReport,
	annotationKey string,
	reason string,
) (*nodeIssueReportv1alpha1.NodeIssueReport, bool, error) {
	actionID := rebootNotificationActionID(nir)
	sendingValue := notificationAnnotationValue(notificationSending, actionID)
	sentValue := notificationAnnotationValue(notificationSent, actionID)

	var current *nodeIssueReportv1alpha1.NodeIssueReport
	shouldSend := false
	err := retryOnConflict(func() error {
		live, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(nir.Namespace).Get(ctx, nir.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		current = live

		// A stale reconcile must not attach an old action's notification state to
		// a newer reboot cycle.
		// 防止 stale reconcile 将旧的 action 的 notification state 附加到新的 reboot cycle 上
		if rebootNotificationActionID(live) != actionID {
			return nil
		}

		value := live.GetAnnotations()[annotationKey]
		if value == sendingValue || value == sentValue {
			return nil
		}

		candidate := live.DeepCopy()
		if candidate.Annotations == nil {
			candidate.Annotations = make(map[string]string)
		}
		candidate.Annotations[annotationKey] = sendingValue
		updated, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(nir.Namespace).Update(ctx, candidate, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		current = updated
		shouldSend = true
		return nil
	})
	if err != nil || !shouldSend {
		return current, false, err
	}

	if err := n.awsOperator.SNSNotify(*current, reason); err != nil {
		latest, stateErr := n.setNotificationState(ctx, current, annotationKey, sendingValue, notificationAnnotationValue(notificationFailed, actionID))
		if stateErr != nil {
			return latest, true, fmt.Errorf("send %s notification: %w (also failed to persist failure state: %v)", reason, err, stateErr)
		}
		return latest, true, fmt.Errorf("send %s notification: %w", reason, err)
	}

	latest, err := n.setNotificationState(ctx, current, annotationKey, sendingValue, sentValue)
	if err != nil {
		return latest, true, fmt.Errorf("persist %s notification state: %w", reason, err)
	}
	return latest, true, nil
}

func (n *NIRController) setNotificationState(
	ctx context.Context,
	nir *nodeIssueReportv1alpha1.NodeIssueReport,
	annotationKey string,
	expectedValue string,
	newValue string,
) (*nodeIssueReportv1alpha1.NodeIssueReport, error) {
	var current *nodeIssueReportv1alpha1.NodeIssueReport
	err := retryOnConflict(func() error {
		live, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(nir.Namespace).Get(ctx, nir.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		current = live
		if live.GetAnnotations()[annotationKey] != expectedValue {
			return nil
		}

		candidate := live.DeepCopy()
		if candidate.Annotations == nil {
			candidate.Annotations = make(map[string]string)
		}
		candidate.Annotations[annotationKey] = newValue
		updated, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(nir.Namespace).Update(ctx, candidate, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		current = updated
		return nil
	})
	return current, err
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
	// Informer cache objects are shared and must never be mutated in place.
	nodeIssueReport = nodeIssueReport.DeepCopy()

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
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, "replace"); err != nil {
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
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, "replace"); err != nil {
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
		nodeobj = nodeobj.DeepCopy()

		// for notready node, do normal drain

		// for unknown status node, do forcely drain
		if nodeIssueReport.Spec.NodeStatus == nodeIssueReportv1alpha1.NodeUnknownStatus {
			err = n.drainNode(nodeobj, true)
			if err != nil {
				n.logger.Errorln("[node newnodejoined phase] fail to drain node forcely:", err)
				n.queue.AddRateLimited(key)
				return true
			}
		} else {
			err = n.drainNode(nodeobj, false)
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
		n.logger.Infoln("[node detached phase] checking if a replacement node has joined for:", nodename)

		// Non-blocking: check if a matching replacement node is already Ready
		newNodeName := n.findReadyReplacementNode(nodeobj)
		if newNodeName == "" {
			// Check if we've been waiting too long (15 minutes since detach)
			detachTime := nodeIssueReport.Spec.LastUpdateTime.Time
			if !detachTime.IsZero() && time.Since(detachTime) > 15*time.Minute {
				n.logger.Errorln("[node detached phase] timed out waiting for replacement node for:", nodename)
				if err := n.awsOperator.SNSNotify(*nodeIssueReport, "replacement-timeout"); err != nil {
					n.logger.Error("[node detached phase] failed to notify admin about timeout:", err)
				}
			}
			// No replacement node yet, requeue and check again later
			n.queue.AddRateLimited(key)
			return true
		}

		n.logger.Infoln("[node detached phase] replacement node ready:", newNodeName)
		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNewJoined
		if _, err = n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Errorln("[node detached phase] failed to change phase to newnodejoined:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node detached phase] phase changed to newnodejoined")
		return true
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

		// Retry a failed reboot-started notification without ever publishing a
		// second copy after a successful/claimed send.
		latestNIR, sent, notifyErr := n.notifyRebootOnce(context.Background(), nodeIssueReport, rebootStartedNotificationAnnotation, "reboot-started")
		if latestNIR != nil {
			nodeIssueReport = latestNIR.DeepCopy()
		}
		if notifyErr != nil {
			n.logger.Errorln("[node rebooted phase] failed to send reboot-started notification:", notifyErr)
		} else if sent {
			n.logger.Infoln("[node rebooted phase] sent reboot-started notification for node:", nodename)
		}
		if nodeIssueReport.Spec.Phase != nodeIssueReportv1alpha1.PhaseRebooted {
			return true
		}

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
		rebootTime := nodeIssueReport.Spec.LastActionTime.Time
		if nodeIssueReport.Spec.LastActionTime.IsZero() {
			// NodeController-triggered reboot: LastActionTime not set, use LastUpdateTime as fallback
			rebootTime = nodeIssueReport.Spec.LastUpdateTime.Time
		}
		if !rebootTime.IsZero() && time.Since(rebootTime) < rebootGracePeriod {
			n.logger.Infof("[node rebooted phase] waiting for reboot to take effect, %v since reboot, grace period %v", time.Since(rebootTime).Round(time.Second), rebootGracePeriod)
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

		latestNIR, sent, notifyErr = n.notifyRebootOnce(context.Background(), nodeIssueReport, rebootCompletedNotificationAnnotation, "reboot-completed")
		if latestNIR != nil {
			nodeIssueReport = latestNIR.DeepCopy()
		}
		if notifyErr != nil {
			n.logger.Error("[node rebooted phase] failed to send reboot-completed notification: ", notifyErr)
		} else if sent {
			n.logger.Infoln("[node rebooted phase] sent reboot-completed notification for node:", nodename)
		}
		if nodeIssueReport.Spec.Phase != nodeIssueReportv1alpha1.PhaseRebooted {
			return true
		}

		// Reboot is not a terminal action - keep NIR for escalation evaluation.
		// Reset phase and action back to None so new events can accumulate scores again.
		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNone
		nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.None
		nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
		if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
			n.logger.Errorln("[node rebooted phase] failed to reset NIR after reboot:", err)
			n.queue.AddRateLimited(key)
			return true
		}
		n.logger.Infoln("[node rebooted phase] reboot completed, NIR reset to PhaseNone for escalation evaluation:", nodeIssueReport.Name)
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
		err = n.drainNode(nodeobj, false)
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

		latestNIR, sent, notifyErr := n.notifyRebootOnce(context.Background(), nodeIssueReport, rebootStartedNotificationAnnotation, "reboot-started")
		if latestNIR != nil {
			nodeIssueReport = latestNIR.DeepCopy()
		}
		if notifyErr != nil {
			n.logger.Error("[node reboot phase] failed to send reboot-started notification: ", notifyErr)
		} else if sent {
			n.logger.Infoln("[node reboot phase] sent reboot-started notification for node:", nodename)
		}
		if nodeIssueReport.Spec.Phase != nodeIssueReportv1alpha1.PhaseReboot {
			return true
		}
		nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseRebooted
		nodeIssueReport.Spec.LastUpdateTime = metav1.Now()

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
		// Get the tolerance config for this node (may be nil for NodeController-triggered actions)
		toleranceEntry, err := n.getToleranceConfigForNode(nodeobj)
		if err != nil {
			n.logger.Errorln("failed to get tolerance config for node:", nodename, "with error:", err)
			n.queue.AddRateLimited(key)
			return true
		}

		// Check allowOperation (only if toleranceEntry exists)
		if toleranceEntry != nil && !toleranceEntry.AllowOperation {
			n.logger.Infoln("ToleranceConfig.allowOperation is false for node:", nodename, ", only notify admin, skip action")
			if err := n.awsOperator.SNSNotify(*nodeIssueReport, "not-allowed"); err != nil {
				n.logger.Error("[node problem detected, skip node] failed to notify admin", err)
				n.queue.AddRateLimited(key)
				return true
			}
			nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNone
			nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.None
			nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
			if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
				n.logger.Errorln("[node problem detected, skip node] failed to reset nodeIssueReport:", err)
				n.queue.AddRateLimited(key)
				return true
			}
			n.logger.Infoln("[node problem detected, skip node] notified admin and reset nodeIssueReport:", nodeIssueReport.Name)
			return true
		}

		if nodeIssueReport.Spec.Phase == nodeIssueReportv1alpha1.PhaseNone {
			// Use the NIR's own Action value (set by NodeController or score bucket overflow).
			// Only override with ToleranceConfig when escalated.
			action := nodeIssueReport.Spec.Action
			if nodeIssueReport.Spec.Escalated && toleranceEntry != nil {
				action = nodeIssueReportv1alpha1.Action(toleranceEntry.EscalateOperation)
				n.logger.Infoln("[escalation] escalating action to:", action, "for node:", nodename)
			}
			n.logger.Infof("[action dispatch] node %s, action=%s, escalated=%v", nodename, action, nodeIssueReport.Spec.Escalated)

			// Dry-run check (only if toleranceEntry exists)
			if toleranceEntry != nil && n.isDryRun(toleranceEntry, nodeIssueReport, string(action)) {
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNone
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.None
				nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
				if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					n.logger.Errorln("[dry-run] failed to reset NIR:", err)
				}
				return true
			}

			// Concurrency check (only if toleranceEntry exists)
			if toleranceEntry != nil && toleranceEntry.MaxConcurrentActions > 0 {
				activeCount := n.countActiveActionsForEntry(toleranceEntry)
				if activeCount >= toleranceEntry.MaxConcurrentActions {
					n.logger.Infof("[concurrency] max concurrent actions reached for entry %s (%d/%d), requeueing node %s",
						toleranceEntry.NodeLabel, activeCount, toleranceEntry.MaxConcurrentActions, nodename)
					n.queue.AddRateLimited(key)
					return true
				}
			}

			switch action {
			case nodeIssueReportv1alpha1.Reboot:
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReboot
			case nodeIssueReportv1alpha1.Replace:
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseReplace
			case nodeIssueReportv1alpha1.Paging:
				n.logger.Infoln("[paging] action is paging, notify admin only for node:", nodename)
				reason := "paging"
				if nodeIssueReport.Spec.Escalated {
					reason = "escalate-paging"
				}
				if err := n.awsOperator.SNSNotify(*nodeIssueReport, reason); err != nil {
					n.logger.Error("[paging] failed to notify admin", err)
					n.queue.AddRateLimited(key)
					return true
				}
				if nodeIssueReport.Spec.Escalated {
					// Escalated paging is a terminal action - delete NIR
					n.logger.Infoln("[paging] escalated paging completed, deleting NIR for node:", nodename)
					if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Delete(context.TODO(), nodeIssueReport.Name, metav1.DeleteOptions{}); err != nil {
						n.logger.Errorln("[paging] failed to delete nodeIssueReport after escalated paging:", err)
						n.queue.AddRateLimited(key)
					}
					return true
				}
				// Non-escalated paging: reset NIR for escalation evaluation
				nodeIssueReport.Spec.Phase = nodeIssueReportv1alpha1.PhaseNone
				nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.None
				nodeIssueReport.Spec.LastUpdateTime = metav1.Now()
				if _, err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(namespace).Update(context.TODO(), nodeIssueReport, metav1.UpdateOptions{}); err != nil {
					n.logger.Errorln("[paging] failed to reset NIR after paging:", err)
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
			metrics.ActionsTotal.WithLabelValues(nodename, string(toleranceEntry.EscalateOperation), "true").Inc()
		} else {
			n.logger.Infof("score bucket overflow for node %s: %d / %d, triggering action: %s",
				nodename, nodeIssueReport.Spec.ScoreInBucket, toleranceEntry.BucketSize, toleranceEntry.Action)
			nodeIssueReport.Spec.Action = nodeIssueReportv1alpha1.Action(toleranceEntry.Action)
			metrics.ActionsTotal.WithLabelValues(nodename, toleranceEntry.Action, "false").Inc()
		}

		nodeIssueReport.Spec.ScoreInBucket = 0
		nodeIssueReport.Spec.LastActionTime = metav1.Now()
		nodeIssueReport.Spec.LastUpdateTime = metav1.Now()

		// Reset score gauge
		nodegroup := "unknown"
		if ng, exists := nodeobj.GetLabels()["eks.amazonaws.com/nodegroup"]; exists {
			nodegroup = ng
		}
		metrics.ScoreBucketCurrent.WithLabelValues(nodename, nodegroup).Set(0)
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

func (n *NIRController) worker() {
	n.logger.Infoln("Running NIRcontorller worker")
	for n.processNextItem() {

	}
}

func (n *NIRController) Run(stopch <-chan struct{}) {
	n.logger.Println("Worker is processing events...")
	if !cache.WaitForCacheSync(stopch, n.nodeInformer.Informer().HasSynced, n.nodeIssueReportInformer.Informer().HasSynced, n.toleranceConfigInformer.Informer().HasSynced) {
		n.logger.Infoln("Timed out waiting for caches to sync")
		return
	}
	for i := 0; i < workercount; i++ {
		go wait.Until(n.worker, time.Second, stopch)
	}

	// Periodic cleanup of expired NIR resources whose cooldown has passed
	go wait.Until(n.cleanupExpiredNIRs, 1*time.Minute, stopch)

	<-stopch
	n.logger.Infoln("Shutting down NIRController")
}

// cleanupExpiredNIRs scans all NodeIssueReport resources and deletes those
// whose cooldown has expired with no pending action (i.e. no escalation triggered).
func (n *NIRController) cleanupExpiredNIRs() {
	nirList, err := n.nodeIssueReportLister.List(labels.Everything())
	if err != nil {
		n.logger.Errorln("[lifecycle cleanup] failed to list NodeIssueReport resources:", err)
		return
	}

	// Update active NIR gauge
	metrics.NIRActive.Set(float64(len(nirList)))

	for _, nir := range nirList {
		if nir.Spec.LastActionTime.IsZero() ||
			nir.Spec.Action != nodeIssueReportv1alpha1.None ||
			nir.Spec.Phase != nodeIssueReportv1alpha1.PhaseNone {
			continue
		}

		nodeobj, err := n.nodelister.Get(nir.Spec.NodeName)
		if err != nil {
			continue
		}
		toleranceEntry, err := n.getToleranceConfigForNode(nodeobj)
		if err != nil || toleranceEntry == nil || toleranceEntry.CooldownTimeInMinutes <= 0 {
			continue
		}

		cooldown := time.Duration(toleranceEntry.CooldownTimeInMinutes) * time.Minute
		if time.Since(nir.Spec.LastActionTime.Time) > cooldown {
			n.logger.Infof("[lifecycle cleanup] cooldown expired for node %s (last action %v ago), cleaning up NIR",
				nir.Spec.NodeName, time.Since(nir.Spec.LastActionTime.Time).Round(time.Second))
			if err := n.awsOperator.SNSNotify(*nir, "cooldown-expired"); err != nil {
				n.logger.Error("[lifecycle cleanup] failed to send final status notification:", err)
			}
			if err := n.nodeIssueReportClient.NodeissuereporterV1alpha1().NodeIssueReports(nir.Namespace).Delete(context.TODO(), nir.Name, metav1.DeleteOptions{}); err != nil {
				n.logger.Errorln("[lifecycle cleanup] failed to delete expired NIR:", nir.Name, err)
			}
		}
	}
}

func NewNIRController(nodeIssueReportInformer nodeIssueReport.NodeIssueReportInformer, toleranceConfigInformer nodeIssueReport.ToleranceConfigInformer, nodeIssueReportClient nirclient.Interface, kubeclient kubernetes.Clientset, awsOperator AWSOperations, nodeInformer informercorev1.NodeInformer) *NIRController {
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

	// Add event handlers to informers here, e.g. n.nodeIssueReportInformer.Informer().AddEventHandler(...)
	return n

}
