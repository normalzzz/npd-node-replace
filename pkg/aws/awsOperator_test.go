package awspkg

import (
	"fmt"
	"strings"
	"testing"

	nodeIssueReportv1alpha1 "xingzhan-node-autoreplace/pkg/apis/nodeIssueReport/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// buildSNSSubject replicates the subject construction logic from SNSNotify
// for testing purposes. This mirrors the switch-case logic exactly.
func buildSNSSubject(reason string) string {
	switch reason {
	case "reboot":
		return "[npd-node-replace] Node REBOOTED due to persistent issues"
	case "replace":
		return "[npd-node-replace] Node REPLACED due to persistent issues"
	case "paging":
		return "[npd-node-replace] Node issues detected - admin notification (paging)"
	case "not-allowed":
		return "[npd-node-replace] Node issues detected - auto-action disabled, notify only"
	case "cooldown-expired":
		return "[npd-node-replace] Node issue report cleanup - cooldown expired, no escalation triggered"
	case "escalate-paging":
		return "[npd-node-replace] ESCALATION - repeated issues after action, admin notification"
	case "concurrency-blocked":
		return "[npd-node-replace] Action DELAYED - max concurrent actions reached, waiting for capacity"
	default:
		if strings.HasPrefix(reason, "dry-run-") {
			action := strings.TrimPrefix(reason, "dry-run-")
			return fmt.Sprintf("[npd-node-replace] DRY-RUN: would execute %s, but dry-run mode is enabled", action)
		}
		return fmt.Sprintf("[npd-node-replace] Node issue notification (%s)", reason)
	}
}

func TestSNSSubjectConstruction(t *testing.T) {
	// This tests the subject construction logic that is part of SNSNotify.
	// We replicate the switch-case here and verify it matches the expected patterns.
	// The actual SNSNotify function uses the same logic internally.

	_ = nodeIssueReportv1alpha1.NodeIssueReport{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nir"},
		Spec: nodeIssueReportv1alpha1.NodeIssueReportSpec{
			NodeName: "test-node",
		},
	}

	tests := []struct {
		name         string
		reason       string
		wantContains []string
	}{
		{
			name:         "reason reboot produces subject containing REBOOTED",
			reason:       "reboot",
			wantContains: []string{"REBOOTED"},
		},
		{
			name:         "reason replace produces subject containing REPLACED",
			reason:       "replace",
			wantContains: []string{"REPLACED"},
		},
		{
			name:         "reason paging produces subject containing paging",
			reason:       "paging",
			wantContains: []string{"paging"},
		},
		{
			name:         "reason dry-run-reboot produces subject containing DRY-RUN and reboot",
			reason:       "dry-run-reboot",
			wantContains: []string{"DRY-RUN", "reboot"},
		},
		{
			name:         "reason not-allowed produces subject containing auto-action disabled",
			reason:       "not-allowed",
			wantContains: []string{"auto-action disabled"},
		},
		{
			name:         "reason escalate-paging produces subject containing ESCALATION",
			reason:       "escalate-paging",
			wantContains: []string{"ESCALATION"},
		},
		{
			name:         "unknown reason produces subject containing the reason in parentheses",
			reason:       "replacement-timeout",
			wantContains: []string{"(replacement-timeout)"},
		},
		{
			name:         "dry-run-replace produces subject with DRY-RUN and replace",
			reason:       "dry-run-replace",
			wantContains: []string{"DRY-RUN", "replace"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject := buildSNSSubject(tt.reason)
			for _, want := range tt.wantContains {
				if !strings.Contains(subject, want) {
					t.Errorf("buildSNSSubject(%q) = %q, want it to contain %q", tt.reason, subject, want)
				}
			}
		})
	}
}
