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
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeIssueReport is a specification for a NodeIssueReport resource
// type Reason string
type NodeIssueReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec NodeIssueReportSpec `json:"spec"`
	// Status NodeIssueReportStatus `json:"status"`
}

// NodeIssueReportSpec is the spec for a NodeIssueReport resource
type NodeIssueReportSpec struct {
	NodeName     string                   `json:"nodename"`
	NodeProblems map[string]ProblemRecord `json:"nodeproblems"`
	NodeStatus   NodeStatus               `json:"nodestatus"`
	Action       Action                   `json:"action"`
	// ForceAction  bool                     `json:"force"`
	Phase 		 Phase 					  `json:"phase"`
}

// NodeIssueReportStatus is the status for a NodeIssueReport resource
// type NodeIssueReportStatus struct {
// 	AvailableReplicas int32 `json:"availableReplicas"`
// }

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeIssueReportList is a list of NodeIssueReport resources
type NodeIssueReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NodeIssueReport `json:"items"`
}

type ProblemRecord struct {
	//Reason string `json:"reason"`
	// Count   int32    `json:"count"`
	Message []MessageEntry `json:"messages"`
}

type MessageEntry struct {
	Timestamp metav1.Time `json:"timestamp"`
	Message   string      `json:"message"`
}

type Action string
const (
	Reboot  Action = "reboot"
	Replace Action = "replace"
	None    Action = "none"
)

const (
	NodeNotReadyStatus NodeStatus  = "NotReady"
	NodeReadyStatus   NodeStatus = "Ready"
	NodeUnknownStatus NodeStatus = "Unknown"
)

type NodeStatus string

type Phase string
const (
	PhaseNone Phase = "phasenone"
	PhaseReplace Phase = "phasereplace"
	PhaseReboot Phase = "phasereboot"
	PhaseRebooted Phase = "phaserebooted"
	PhaseDetached Phase = "phasedetached"
	PhaseNewJoined Phase = "phaseneejoined"
	PhaseDrained Phase = "phasedrained"
)