package controller

import (
	"fmt"
	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	nirlister "xingzhan-node-autoreplace/pkg/generated/listers/nodeissuereport/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	listercorev1 "k8s.io/client-go/listers/core/v1"
)

// --- Fake ToleranceConfigLister ---
// Implements nirlister.ToleranceConfigLister

type fakeToleranceConfigLister struct {
	configs []*nodeIssueReportv1alpha1.ToleranceConfig
}

func (f *fakeToleranceConfigLister) List(selector labels.Selector) (ret []*nodeIssueReportv1alpha1.ToleranceConfig, err error) {
	return f.configs, nil
}

func (f *fakeToleranceConfigLister) Get(name string) (*nodeIssueReportv1alpha1.ToleranceConfig, error) {
	for _, tc := range f.configs {
		if tc.Name == name {
			return tc, nil
		}
	}
	return nil, fmt.Errorf("toleranceconfig %q not found", name)
}

// Verify interface compliance
var _ nirlister.ToleranceConfigLister = &fakeToleranceConfigLister{}

// --- Fake NodeIssueReportLister ---
// Implements nirlister.NodeIssueReportLister

type fakeNodeIssueReportLister struct {
	nirs []*nodeIssueReportv1alpha1.NodeIssueReport
}

func (f *fakeNodeIssueReportLister) List(selector labels.Selector) (ret []*nodeIssueReportv1alpha1.NodeIssueReport, err error) {
	return f.nirs, nil
}

func (f *fakeNodeIssueReportLister) NodeIssueReports(namespace string) nirlister.NodeIssueReportNamespaceLister {
	var filtered []*nodeIssueReportv1alpha1.NodeIssueReport
	for _, nir := range f.nirs {
		if nir.Namespace == namespace {
			filtered = append(filtered, nir)
		}
	}
	return &fakeNodeIssueReportNamespaceLister{nirs: filtered}
}

// Verify interface compliance
var _ nirlister.NodeIssueReportLister = &fakeNodeIssueReportLister{}

// --- Fake NodeIssueReportNamespaceLister ---
// Implements nirlister.NodeIssueReportNamespaceLister

type fakeNodeIssueReportNamespaceLister struct {
	nirs []*nodeIssueReportv1alpha1.NodeIssueReport
}

func (f *fakeNodeIssueReportNamespaceLister) List(selector labels.Selector) (ret []*nodeIssueReportv1alpha1.NodeIssueReport, err error) {
	return f.nirs, nil
}

func (f *fakeNodeIssueReportNamespaceLister) Get(name string) (*nodeIssueReportv1alpha1.NodeIssueReport, error) {
	for _, nir := range f.nirs {
		if nir.Name == name {
			return nir, nil
		}
	}
	return nil, fmt.Errorf("nodeissuereport %q not found", name)
}

// Verify interface compliance
var _ nirlister.NodeIssueReportNamespaceLister = &fakeNodeIssueReportNamespaceLister{}

// --- Fake NodeLister ---
// Implements listercorev1.NodeLister

type fakeNodeLister struct {
	nodes []*corev1.Node
}

func (f *fakeNodeLister) List(selector labels.Selector) (ret []*corev1.Node, err error) {
	return f.nodes, nil
}

func (f *fakeNodeLister) Get(name string) (*corev1.Node, error) {
	for _, n := range f.nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return nil, fmt.Errorf("node %q not found", name)
}

// Verify interface compliance
var _ listercorev1.NodeLister = &fakeNodeLister{}

// --- Mock AwsOperator ---
// Records SNS calls for verification

type snsCall struct {
	NIR    nodeIssueReportv1alpha1.NodeIssueReport
	Reason string
}

type mockAwsOperator struct {
	snsNotifyCalls []snsCall
	snsErr         error
}

func (m *mockAwsOperator) SNSNotify(nir nodeIssueReportv1alpha1.NodeIssueReport, reason string) error {
	m.snsNotifyCalls = append(m.snsNotifyCalls, snsCall{NIR: nir, Reason: reason})
	return m.snsErr
}

// --- Helper Functions ---

func newTestNode(name string, nodeLabels map[string]string, conditions []corev1.NodeCondition, owners []metav1.OwnerReference) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Labels:          nodeLabels,
			OwnerReferences: owners,
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
		},
	}
}

func newTestNodeWithCreationTime(name string, nodeLabels map[string]string, conditions []corev1.NodeCondition, creationTime metav1.Time) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            nodeLabels,
			CreationTimestamp: creationTime,
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
		},
	}
}

func readyCondition(status corev1.ConditionStatus) corev1.NodeCondition {
	return corev1.NodeCondition{
		Type:   corev1.NodeReady,
		Status: status,
	}
}
