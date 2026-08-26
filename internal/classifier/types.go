package classifier

import (
	"context"
	"time"
)

const (
	// These are executable actions.
	ActionRestartPod  = "restart_pod"
	ActionRolloutUndo = "rollout_undo"

	// This is a decision, not an executable Kubernetes action.
	ActionEscalateToHuman = "escalate_to_human"

	DecisionAutomate     = "automate"
	DecisionEscalate     = "escalate"
	DecisionFallbackMiss = "fallback_miss"
)

const (
	ReasonCodeNone                      = ""
	ReasonCodeUnsupportedSubCause       = "unsupported_sub_cause"
	ReasonCodeUnsupportedAction         = "unsupported_action"
	ReasonCodeMissingTargetKind         = "missing_target_kind"
	ReasonCodeMissingTargetNamespace    = "missing_target_namespace"
	ReasonCodeMissingTargetName         = "missing_target_name"
	ReasonCodeMissingReasoning          = "missing_reasoning"
	ReasonCodeNamespaceMismatch         = "namespace_mismatch"
	ReasonCodeUnsafeExecutableAction    = "unsafe_executable_action"
	ReasonCodeSemanticGuardRejected     = "semantic_guard_rejected"
	ReasonCodeMissingDeploymentEvidence = "missing_deployment_evidence"
	ReasonCodeWrongTargetKind           = "wrong_target_kind"
	ReasonCodeWrongTargetName           = "wrong_target_name"
	ReasonCodeMissingOwnerDeployment    = "missing_owner_deployment"
	ReasonCodeEscalationRequired        = "escalation_required"
)

// DetectionEvent is the contract received from Owner 1.
type DetectionEvent struct {
	PodName         string    `json:"pod_name"`
	Namespace       string    `json:"namespace"`
	ContainerName   string    `json:"container_name"`
	RestartCount    int32     `json:"restart_count"`
	OwnerDeployment string    `json:"owner_deployment"`
	Timestamp       time.Time `json:"timestamp"`
}

// IncidentInput contains the detection event plus the evidence
// required by the LLM classifier.
type IncidentInput struct {
	DetectionEvent

	Logs   string   `json:"logs"`
	Events []string `json:"events"`
}

// Target identifies the Kubernetes resource related to the decision.
type Target struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Proposal is the structured triage result returned by the LLM.
type Proposal struct {
	SubCause          string `json:"sub_cause"`
	RecommendedAction string `json:"recommended_action"`
	Target            Target `json:"target"`

	// True means the validator may consider passing the action
	// into the automated remediation pipeline.
	SafeForAutomation bool `json:"safe_for_automation"`

	Reasoning string `json:"reasoning"`
}

// ValidationResult represents one of three outcomes:
//
//  1. automate
//  2. escalate
//  3. fallback_miss
//
// Valid=true means that the classifier output is structurally and
// semantically valid. It does not necessarily mean an action should run.
type ValidationResult struct {
	Valid      bool     `json:"valid"`
	Decision   string   `json:"decision"`
	Reason     string   `json:"reason,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
	Output     Proposal `json:"output"`
}

// Classifier defines the function every classifier must implement.
type Classifier interface {
	Classify(
		ctx context.Context,
		input IncidentInput,
	) (Proposal, error)
}

type ClassifierCallMetadata struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type MetadataClassifier interface {
	Classifier

	ClassifyWithMetadata(
		ctx context.Context,
		input IncidentInput,
	) (
		Proposal,
		ClassifierCallMetadata,
		error,
	)
}
