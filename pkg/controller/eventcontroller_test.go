package controller

import (
	"testing"
	"time"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsNodeProblemDetectorEvent(t *testing.T) {
	controllerStart := metav1.Time{Time: time.Now().Add(-1 * time.Hour)}
	afterStart := metav1.Time{Time: time.Now().Add(-30 * time.Minute)}
	beforeStart := metav1.Time{Time: time.Now().Add(-2 * time.Hour)}

	regularNode := newTestNode("node-1", map[string]string{"eks.amazonaws.com/nodegroup": "ng-1"}, nil, nil)
	karpenterNode := newTestNode("karpenter-node", nil, nil, []metav1.OwnerReference{
		{Kind: "NodeClaim", Name: "nc-1"},
	})
	fargateNode := newTestNode("fargate-node", map[string]string{"eks.amazonaws.com/compute-type": "fargate"}, nil, nil)

	nodeLister := &fakeNodeLister{nodes: []*corev1.Node{regularNode, karpenterNode, fargateNode}}

	c := &EventController{
		NodeLister:          nodeLister,
		controllerStartTime: controllerStart,
		logger:              *log.WithField("component", "test"),
	}

	tests := []struct {
		name     string
		event    *corev1.Event
		expected bool
	}{
		{
			name: "known NPD source component (kernel-monitor) returns true",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-1"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Source:         corev1.EventSource{Component: "kernel-monitor"},
				LastTimestamp:  afterStart,
			},
			expected: true,
		},
		{
			name: "InvolvedObject.Kind != Node returns false",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-2"},
				InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "pod-1"},
				Source:         corev1.EventSource{Component: "kernel-monitor"},
				LastTimestamp:  afterStart,
			},
			expected: false,
		},
		{
			name: "event before controller start time returns false",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-3"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Source:         corev1.EventSource{Component: "kernel-monitor"},
				LastTimestamp:  beforeStart,
			},
			expected: false,
		},
		{
			name: "event targeting Karpenter node returns false",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-4"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "karpenter-node"},
				Source:         corev1.EventSource{Component: "kernel-monitor"},
				LastTimestamp:  afterStart,
			},
			expected: false,
		},
		{
			name: "event targeting Fargate node returns false",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-5"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "fargate-node"},
				Source:         corev1.EventSource{Component: "kernel-monitor"},
				LastTimestamp:  afterStart,
			},
			expected: false,
		},
		{
			name: "ManagedFields containing node-problem-detector returns true",
			event: &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name: "evt-6",
					ManagedFields: []metav1.ManagedFieldsEntry{
						{Manager: "node-problem-detector"},
					},
				},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Source:         corev1.EventSource{Component: "unknown-component"},
				LastTimestamp:  afterStart,
			},
			expected: true,
		},
		{
			name: "unrecognized source and no NPD managed fields returns false",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-7"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Source:         corev1.EventSource{Component: "kubelet"},
				LastTimestamp:  afterStart,
			},
			expected: false,
		},
		{
			name: "disk-monitor source returns true",
			event: &corev1.Event{
				ObjectMeta:     metav1.ObjectMeta{Name: "evt-8"},
				InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: "node-1"},
				Source:         corev1.EventSource{Component: "disk-monitor"},
				LastTimestamp:  afterStart,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.isNodeProblemDetectorEvent(tt.event)
			if got != tt.expected {
				t.Errorf("isNodeProblemDetectorEvent() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetEventScoreForNode(t *testing.T) {
	node := newTestNode("node-1", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	unmatchedNode := newTestNode("node-2", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-other",
	}, nil, nil)

	nodeLister := &fakeNodeLister{nodes: []*corev1.Node{node, unmatchedNode}}

	toleranceConfigs := []*nodeIssueReportv1alpha1.ToleranceConfig{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
			Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
				Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{
					{
						NodeLabel:  "eks.amazonaws.com/nodegroup=ng-1",
						BucketSize: 10,
						EventScores: []nodeIssueReportv1alpha1.EventScore{
							{EventName: "OOMKilling", Score: 5},
							{EventName: "KernelOops", Score: 3},
						},
					},
				},
			},
		},
	}

	c := &EventController{
		NodeLister:            nodeLister,
		toleranceConfigLister: &fakeToleranceConfigLister{configs: toleranceConfigs},
		logger:                *log.WithField("component", "test"),
	}

	tests := []struct {
		name      string
		nodeName  string
		reason    string
		wantScore int32
	}{
		{
			name:      "matching node and existing reason returns configured score",
			nodeName:  "node-1",
			reason:    "OOMKilling",
			wantScore: 5,
		},
		{
			name:      "matching node but reason not in EventScores returns 0",
			nodeName:  "node-1",
			reason:    "UnknownReason",
			wantScore: 0,
		},
		{
			name:      "node labels do not match any entry returns 0",
			nodeName:  "node-2",
			reason:    "OOMKilling",
			wantScore: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.getEventScoreForNode(tt.nodeName, tt.reason)
			if got != tt.wantScore {
				t.Errorf("getEventScoreForNode(%s, %s) = %d, want %d", tt.nodeName, tt.reason, got, tt.wantScore)
			}
		})
	}

	// Test with empty tolerance configs
	t.Run("no ToleranceConfig resources returns 0", func(t *testing.T) {
		emptyC := &EventController{
			NodeLister:            nodeLister,
			toleranceConfigLister: &fakeToleranceConfigLister{configs: nil},
			logger:                *log.WithField("component", "test"),
		}
		got := emptyC.getEventScoreForNode("node-1", "OOMKilling")
		if got != 0 {
			t.Errorf("getEventScoreForNode() with no configs = %d, want 0", got)
		}
	})
}

func TestRecalcScoreInBucket(t *testing.T) {
	now := time.Now()
	windowMinutes := int32(30)

	node := newTestNode("node-1", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	nodeLister := &fakeNodeLister{nodes: []*corev1.Node{node}}

	entry := &nodeIssueReportv1alpha1.ToleranceConfigEntry{
		NodeLabel:            "eks.amazonaws.com/nodegroup=ng-1",
		BucketSize:           10,
		EventWindowInMinutes: windowMinutes,
		EventScores: []nodeIssueReportv1alpha1.EventScore{
			{EventName: "OOMKilling", Score: 5},
			{EventName: "KernelOops", Score: 3},
		},
	}

	c := &EventController{
		NodeLister: nodeLister,
		logger:     *log.WithField("component", "test"),
	}

	tests := []struct {
		name      string
		nir       *nodeIssueReportv1alpha1.NodeIssueReport
		wantScore int32
	}{
		{
			name: "all events within window returns sum of matching scores",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeName: "node-1",
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"OOMKilling": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-10 * time.Minute)}, Message: "oom1"},
								{Timestamp: metav1.Time{Time: now.Add(-5 * time.Minute)}, Message: "oom2"},
							},
						},
						"KernelOops": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-15 * time.Minute)}, Message: "oops1"},
							},
						},
					},
				},
			},
			wantScore: 5 + 5 + 3, // 2 OOMKilling + 1 KernelOops
		},
		{
			name: "events outside window are excluded",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeName: "node-1",
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"OOMKilling": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-60 * time.Minute)}, Message: "old event"},
								{Timestamp: metav1.Time{Time: now.Add(-5 * time.Minute)}, Message: "recent event"},
							},
						},
					},
				},
			},
			wantScore: 5, // only the recent one
		},
		{
			name: "LastActionTime as lower bound excludes earlier events",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeName:       "node-1",
					LastActionTime: metav1.Time{Time: now.Add(-10 * time.Minute)},
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"OOMKilling": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-20 * time.Minute)}, Message: "before action"},
								{Timestamp: metav1.Time{Time: now.Add(-5 * time.Minute)}, Message: "after action"},
							},
						},
					},
				},
			},
			wantScore: 5, // only the one after LastActionTime
		},
		{
			name: "no matching event reasons returns 0",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeName: "node-1",
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"UnknownReason": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-5 * time.Minute)}, Message: "unknown"},
							},
						},
					},
				},
			},
			wantScore: 0,
		},
		{
			name: "mixed events - some in window, some outside",
			nir: &nodeIssueReportv1alpha1.NodeIssueReport{
				Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
					NodeName: "node-1",
					NodeProblems: map[string]nodeIssueReportv1alpha1.ProblemRecord{
						"OOMKilling": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-60 * time.Minute)}, Message: "expired"},
								{Timestamp: metav1.Time{Time: now.Add(-10 * time.Minute)}, Message: "in-window"},
							},
						},
						"KernelOops": {
							Message: []nodeIssueReportv1alpha1.MessageEntry{
								{Timestamp: metav1.Time{Time: now.Add(-2 * time.Minute)}, Message: "in-window"},
								{Timestamp: metav1.Time{Time: now.Add(-45 * time.Minute)}, Message: "expired"},
							},
						},
					},
				},
			},
			wantScore: 5 + 3, // 1 OOMKilling + 1 KernelOops in window
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.recalcScoreInBucket(tt.nir, entry)
			if got != tt.wantScore {
				t.Errorf("recalcScoreInBucket() = %d, want %d", got, tt.wantScore)
			}
		})
	}
}

func TestConstructNodeIssueReport(t *testing.T) {
	node := newTestNode("node-1", map[string]string{
		"eks.amazonaws.com/nodegroup": "ng-1",
	}, nil, nil)
	nodeLister := &fakeNodeLister{nodes: []*corev1.Node{node}}

	toleranceConfigs := []*nodeIssueReportv1alpha1.ToleranceConfig{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "tc-1"},
			Spec: nodeIssueReportv1alpha1.ToleranceConfigSpec{
				Configs: []nodeIssueReportv1alpha1.ToleranceConfigEntry{
					{
						NodeLabel:  "eks.amazonaws.com/nodegroup=ng-1",
						BucketSize: 10,
						EventScores: []nodeIssueReportv1alpha1.EventScore{
							{EventName: "OOMKilling", Score: 5},
						},
					},
				},
			},
		},
	}

	c := &EventController{
		NodeLister:            nodeLister,
		toleranceConfigLister: &fakeToleranceConfigLister{configs: toleranceConfigs},
		logger:                *log.WithField("component", "test"),
	}

	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "evt-1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Node",
			Name:      "node-1",
			Namespace: "default",
		},
		Reason:        "OOMKilling",
		Message:       "OOM detected",
		LastTimestamp: metav1.Time{Time: time.Now()},
	}

	t.Run("NIR has correct NodeName from event", func(t *testing.T) {
		nir := c.constructNodeIssueReport(event)
		if nir.Spec.NodeName != "node-1" {
			t.Errorf("NodeName = %s, want node-1", nir.Spec.NodeName)
		}
	})

	t.Run("NIR has Phase=PhaseNone and Action=None", func(t *testing.T) {
		nir := c.constructNodeIssueReport(event)
		if nir.Spec.Phase != nodeIssueReportv1alpha1.PhaseNone {
			t.Errorf("Phase = %s, want %s", nir.Spec.Phase, nodeIssueReportv1alpha1.PhaseNone)
		}
		if nir.Spec.Action != nodeIssueReportv1alpha1.None {
			t.Errorf("Action = %s, want %s", nir.Spec.Action, nodeIssueReportv1alpha1.None)
		}
	})

	t.Run("NIR has initial ScoreInBucket from configured score", func(t *testing.T) {
		nir := c.constructNodeIssueReport(event)
		if nir.Spec.ScoreInBucket != 5 {
			t.Errorf("ScoreInBucket = %d, want 5", nir.Spec.ScoreInBucket)
		}
	})

	t.Run("NIR NodeProblems has exactly one entry keyed by event Reason", func(t *testing.T) {
		nir := c.constructNodeIssueReport(event)
		if len(nir.Spec.NodeProblems) != 1 {
			t.Errorf("NodeProblems has %d entries, want 1", len(nir.Spec.NodeProblems))
		}
		record, exists := nir.Spec.NodeProblems["OOMKilling"]
		if !exists {
			t.Error("NodeProblems does not contain key 'OOMKilling'")
		}
		if len(record.Message) != 1 {
			t.Errorf("Messages has %d entries, want 1", len(record.Message))
		}
	})
}
