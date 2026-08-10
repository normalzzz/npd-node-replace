package controller

import (
	"os"
	"testing"
	"time"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"
	awspkg "xingzhan-node-autoreplace/pkg/aws"

	"github.com/aws/aws-sdk-go-v2/aws"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetToleranceConfigForNode(t *testing.T) {
	node := newTestNode("node-1", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	unmatchedNode := newTestNode("node-2", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-other",
	}, nil, nil)
	nodeLister := &fakeNodeLister{nodes: []*corev1.Node{node, unmatchedNode}}

	entry1 := nodeIssueReportv1alpha1.ToleranceConfigEntry{
		NodeLabel:  "eks.amazonaws.com/nodegroup=ng-1",
		BucketSize: 10,
		Action:     "reboot",
	}
	entry2 := nodeIssueReportv1alpha1.ToleranceConfigEntry{
		NodeLabel:  "eks.amazonaws.com/nodegroup=ng-2",
		BucketSize: 20,
		Action:     "replace",
	}
	malformedEntry := nodeIssueReportv1alpha1.ToleranceConfigEntry{
		NodeLabel:  "malformed-no-equals",
		BucketSize: 5,
		Action:     "reboot",
	}

	tests := []struct {
		name       string
		nodeName   string
		configs    []*nodeIssueReportv1alpha1.ToleranceConfig
		wantNil    bool
		wantAction string
	}{
		{
			name:     "node labels match a ToleranceConfigEntry returns the entry",
			nodeName: "node-1",
			configs: []*nodeIssueReportv1alpha1.ToleranceConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
					Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
						Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{entry1},
					},
				},
			},
			wantNil:    false,
			wantAction: "reboot",
		},
		{
			name:     "node labels do not match any entry returns nil",
			nodeName: "node-2",
			configs: []*nodeIssueReportv1alpha1.ToleranceConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
					Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
						Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{entry1},
					},
				},
			},
			wantNil: true,
		},
		{
			name:     "multiple entries - returns first match",
			nodeName: "node-1",
			configs: []*nodeIssueReportv1alpha1.ToleranceConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
					Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
						Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{entry1, entry2},
					},
				},
			},
			wantNil:    false,
			wantAction: "reboot",
		},
		{
			name:     "malformed NodeLabel is skipped",
			nodeName: "node-1",
			configs: []*nodeIssueReportv1alpha1.ToleranceConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
					Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
						Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{malformedEntry, entry1},
					},
				},
			},
			wantNil:    false,
			wantAction: "reboot",
		},
		{
			name:     "no ToleranceConfig resources returns nil",
			nodeName: "node-1",
			configs:  nil,
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &NIRController{
				toleranceConfigLister: &fakeToleranceConfigLister{configs: tt.configs},
				nodelister:            nodeLister,
				logger:                *log.WithField("component", "test"),
			}
			nodeobj, _ := nodeLister.Get(tt.nodeName)
			got, err := ctrl.getToleranceConfigForNode(nodeobj)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
			} else {
				if got == nil {
					t.Fatal("expected non-nil entry, got nil")
				}
				if got.Action != tt.wantAction {
					t.Errorf("Action = %s, want %s", got.Action, tt.wantAction)
				}
			}
		})
	}
}

func TestFindReadyReplacementNode(t *testing.T) {
	now := time.Now()
	oldNode := newTestNodeWithCreationTime("old-node", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, []corev1.NodeCondition{readyCondition(corev1.ConditionTrue)}, metav1.Time{Time: now.Add(-1 * time.Hour)})

	recentReadyNode := newTestNodeWithCreationTime("new-node", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, []corev1.NodeCondition{readyCondition(corev1.ConditionTrue)}, metav1.Time{Time: now.Add(-5 * time.Minute)})

	differentNodegroup := newTestNodeWithCreationTime("other-ng-node", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-2",
	}, []corev1.NodeCondition{readyCondition(corev1.ConditionTrue)}, metav1.Time{Time: now.Add(-5 * time.Minute)})

	notReadyNode := newTestNodeWithCreationTime("not-ready-node", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, []corev1.NodeCondition{readyCondition(corev1.ConditionFalse)}, metav1.Time{Time: now.Add(-5 * time.Minute)})

	tests := []struct {
		name     string
		nodes    []*corev1.Node
		oldNode  *corev1.Node
		wantName string
	}{
		{
			name:     "matching node (same nodegroup, recent, Ready) returns its name",
			nodes:    []*corev1.Node{oldNode, recentReadyNode},
			oldNode:  oldNode,
			wantName: "new-node",
		},
		{
			name:     "no matching nodes returns empty string",
			nodes:    []*corev1.Node{oldNode, notReadyNode},
			oldNode:  oldNode,
			wantName: "",
		},
		{
			name:     "node in different nodegroup is rejected",
			nodes:    []*corev1.Node{oldNode, differentNodegroup},
			oldNode:  oldNode,
			wantName: "",
		},
		{
			name:     "old node itself is skipped",
			nodes:    []*corev1.Node{oldNode},
			oldNode:  oldNode,
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &NIRController{
				nodelister: &fakeNodeLister{nodes: tt.nodes},
				logger:     *log.WithField("component", "test"),
			}
			got := ctrl.findReadyReplacementNode(tt.oldNode)
			if got != tt.wantName {
				t.Errorf("findReadyReplacementNode() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestCountActiveActionsForEntry(t *testing.T) {
	node1 := newTestNode("node-1", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	node2 := newTestNode("node-2", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	node3 := newTestNode("node-3", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-2",
	}, nil, nil)

	entry := &nodeIssueReportv1alpha1.ToleranceConfigEntry{
		NodeLabel: "eks.amazonaws.com/nodegroup=ng-1",
	}

	tests := []struct {
		name  string
		nirs  []*nodeIssueReportv1alpha1.NodeIssueReport
		nodes []*corev1.Node
		want  int32
	}{
		{
			name: "counts only active-phase NIRs matching the label",
			nirs: []*nodeIssueReportv1alpha1.NodeIssueReport{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nir-1", Namespace: "default"},
					Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
						NodeName: "node-1",
						Phase:    nodeIssueReportv1alpha1.PhaseReboot,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nir-2", Namespace: "default"},
					Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
						NodeName: "node-2",
						Phase:    nodeIssueReportv1alpha1.PhaseReplace,
					},
				},
			},
			nodes: []*corev1.Node{node1, node2},
			want:  2,
		},
		{
			name: "returns 0 when all NIRs are PhaseNone",
			nirs: []*nodeIssueReportv1alpha1.NodeIssueReport{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nir-1", Namespace: "default"},
					Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
						NodeName: "node-1",
						Phase:    nodeIssueReportv1alpha1.PhaseNone,
					},
				},
			},
			nodes: []*corev1.Node{node1},
			want:  0,
		},
		{
			name: "returns 0 when active NIR nodes don't match label",
			nirs: []*nodeIssueReportv1alpha1.NodeIssueReport{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "nir-3", Namespace: "default"},
					Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
						NodeName: "node-3",
						Phase:    nodeIssueReportv1alpha1.PhaseReboot,
					},
				},
			},
			nodes: []*corev1.Node{node3},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &NIRController{
				nodeIssueReportLister: &fakeNodeIssueReportLister{nirs: tt.nirs},
				nodelister:            &fakeNodeLister{nodes: tt.nodes},
				logger:                *log.WithField("component", "test"),
			}
			got := ctrl.countActiveActionsForEntry(entry)
			if got != tt.want {
				t.Errorf("countActiveActionsForEntry() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsDryRun(t *testing.T) {
	// For isDryRun, since awsOperator is a concrete struct and SNSNotify needs a real
	// SNS client, we test only the boolean return value. When DryRun=true, isDryRun
	// returns true regardless of SNS notification success/failure.
	// We set SNS_TOPIC_ARN to avoid empty topic issues and create a real AwsOperator
	// with a dummy config so snscli is not nil (it will fail on Publish but isDryRun
	// catches the error).
	os.Setenv("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:123456789:test-topic")
	defer os.Unsetenv("SNS_TOPIC_ARN")

	nir := &nodeIssueReportv1alpha1.NodeIssueReport{
		ObjectMeta: metav1.ObjectMeta{Name: "nir-1", Namespace: "default"},
		Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
			NodeName: "node-1",
			Phase:    nodeIssueReportv1alpha1.PhaseNone,
		},
	}

	// Create a real AwsOperator with a dummy config so snscli is initialized (non-nil)
	dummyOp := awspkg.NewAwsOperator(aws.Config{Region: "us-east-1"})

	t.Run("DryRun=true returns true", func(t *testing.T) {
		entry := &nodeIssueReportv1alpha1.ToleranceConfigEntry{
			DryRun: true,
		}
		ctrl := &NIRController{
			awsOperator: *dummyOp,
			logger:      *log.WithField("component", "test"),
		}
		got := ctrl.isDryRun(entry, nir, "reboot")
		if !got {
			t.Error("isDryRun() = false, want true when DryRun=true")
		}
	})

	t.Run("DryRun=false returns false", func(t *testing.T) {
		entry := &nodeIssueReportv1alpha1.ToleranceConfigEntry{
			DryRun: false,
		}
		ctrl := &NIRController{
			awsOperator: *dummyOp,
			logger:      *log.WithField("component", "test"),
		}
		got := ctrl.isDryRun(entry, nir, "reboot")
		if got {
			t.Error("isDryRun() = true, want false when DryRun=false")
		}
	})
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name       string
		conditions []corev1.NodeCondition
		want       bool
	}{
		{
			name:       "Ready=True returns true",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionTrue)},
			want:       true,
		},
		{
			name:       "Ready=False returns false",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionFalse)},
			want:       false,
		},
		{
			name:       "Ready=Unknown returns false",
			conditions: []corev1.NodeCondition{readyCondition(corev1.ConditionUnknown)},
			want:       false,
		},
		{
			name:       "no Ready condition returns false",
			conditions: []corev1.NodeCondition{},
			want:       false,
		},
	}

	ctrl := &NIRController{
		logger: *log.WithField("component", "test"),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &corev1.Node{
				Status: corev1.NodeStatus{Conditions: tt.conditions},
			}
			got := ctrl.isNodeReady(node)
			if got != tt.want {
				t.Errorf("isNodeReady() = %v, want %v", got, tt.want)
			}
		})
	}
}
