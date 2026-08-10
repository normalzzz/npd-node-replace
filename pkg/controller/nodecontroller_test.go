package controller

import (
	"testing"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsManagedByASG(t *testing.T) {
	c := &NodeController{
		logger: *log.WithField("component", "test"),
	}

	tests := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "Karpenter node (NodeClaim owner) returns false",
			node: newTestNode("karpenter-node", nil, nil, []metav1.OwnerReference{
				{Kind: "NodeClaim", Name: "nc-1"},
			}),
			want: false,
		},
		{
			name: "Fargate node returns false",
			node: newTestNode("fargate-node", map[string]string{
				"eks.amazonaws.com/compute-type": "fargate",
			}, nil, nil),
			want: false,
		},
		{
			name: "regular ASG node returns true",
			node: newTestNode("asg-node", map[string]string{
				"eks.amazonaws.com/nodegroup": "ng-1",
			}, nil, nil),
			want: true,
		},
		{
			name: "node with no special labels or owners returns true",
			node: newTestNode("plain-node", nil, nil, nil),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.isManagedByASG(tt.node)
			if got != tt.want {
				t.Errorf("isManagedByASG() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChecknodestatus(t *testing.T) {
	c := &NodeController{
		logger: *log.WithField("component", "test"),
	}

	tests := []struct {
		name       string
		conditions []corev1.NodeCondition
		wantStatus corev1.ConditionStatus
		wantFound  bool
	}{
		{
			name:       "Ready condition True returns ConditionTrue",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionTrue)},
			wantStatus: corev1.ConditionTrue,
			wantFound:  true,
		},
		{
			name:       "Ready condition False returns ConditionFalse",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionFalse)},
			wantStatus: corev1.ConditionFalse,
			wantFound:  true,
		},
		{
			name:       "Ready condition Unknown returns ConditionUnknown",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionUnknown)},
			wantStatus: corev1.ConditionUnknown,
			wantFound:  true,
		},
		{
			name:       "no Ready condition returns ConditionUnknown, false",
			conditions: []corev1.NodeCondition{},
			wantStatus: corev1.ConditionUnknown,
			wantFound:  false,
		},
		{
			name: "multiple conditions - only Ready matters",
			conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			wantStatus: corev1.ConditionTrue,
			wantFound:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{
				Status: corev1.NodeStatus{Conditions: tt.conditions},
			}
			gotStatus, gotFound := c.checknodestatus(node)
			if gotStatus != tt.wantStatus {
				t.Errorf("checknodestatus() status = %v, want %v", gotStatus, tt.wantStatus)
			}
			if gotFound != tt.wantFound {
				t.Errorf("checknodestatus() found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}

func TestCheckIfNodeIssueReportHaveIssueRecorded(t *testing.T) {
	c := &NodeController{
		logger: *log.WithField("component", "test"),
	}

	tests := []struct {
		name string
		nir  *nodeIssueReportv1alpha1.NodeIssueReport
		want bool
	}{
		{
			name: "empty NodeProblems returns false",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{},
				},
			},
			want: false,
		},
		{
			name: "non-empty NodeProblems returns true",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"OOMKilling": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Message: "oom detected"},
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.checkIfNodeIssueReportHaveIssueRecorded(tt.nir)
			if got != tt.want {
				t.Errorf("checkIfNodeIssueReportHaveIssueRecorded() = %v, want %v", got, tt.want)
			}
		})
	}
}
