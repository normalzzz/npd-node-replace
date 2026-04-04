/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster

// ToleranceConfig defines tolerance policies for a group of nodes identified by labels.
type ToleranceConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ToleranceConfigSpec `json:"spec"`
}

// ToleranceConfigSpec is the spec for a ToleranceConfig resource.
type ToleranceConfigSpec struct {
	// Configs is the list of tolerance configurations, each targeting a set of nodes by label.
	Configs []ToleranceConfigEntry `json:"configs"`
}

// ToleranceConfigEntry defines tolerance policy for nodes matching a specific label.
type ToleranceConfigEntry struct {
	// NodeLabel is the label selector in "key=value" format to match target nodes.
	NodeLabel string `json:"nodeLabel"`

	// BucketSize is the score threshold. When accumulated score reaches this value, the action is triggered.
	BucketSize int32 `json:"bucketSize"`

	// Action is the action to take when the bucket overflows. Valid values are "reboot", "replace" or "paging".
	Action string `json:"action"`

	// AllowOperation indicates whether npd-node-replace is allowed to perform actions on problem nodes.
	// If false, only SNS notification is sent to the admin without any reboot/replace action.
	AllowOperation bool `json:"allowOperation"`

	// CooldownTimeInMinutes is the cooldown window (in minutes) after an action is performed.
	// If the score bucket fills up again within this window, the escalation operation is triggered.
	// +optional
	CooldownTimeInMinutes int32 `json:"cooldownTimeInMinutes,omitempty"`

	// EventWindowInMinutes is the sliding time window (in minutes) for scoring events.
	// Only events whose timestamp falls within [now - EventWindowInMinutes, now] are counted
	// toward the score bucket. Events older than this window are considered expired and ignored.
	EventWindowInMinutes int32 `json:"eventWindowInMinutes"`

	// EscalateOperation defines the escalation action when the bucket overflows again within the cooldown window.
	// Valid values are "replace" or "paging".
	// +optional
	EscalateOperation EscalationAction `json:"escalateOperation,omitempty"`

	// EventScores defines the score for each event type.
	EventScores []EventScore `json:"eventScores"`
}

// EventScore maps an event name to a score value.
type EventScore struct {
	// EventName is the NPD event reason name (e.g. "OOMKilling", "KernelOops").
	EventName string `json:"eventName"`

	// Score is the score value assigned to this event type.
	Score int32 `json:"score"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ToleranceConfigList is a list of ToleranceConfig resources.
type ToleranceConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ToleranceConfig `json:"items"`
}

type EscalationAction string
const (
	replace EscalationAction = "replace"
	paging EscalationAction = "paging"
	none EscalationAction = "none"
)