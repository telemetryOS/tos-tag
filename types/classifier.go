package types

type ParticipationMode string

const (
	ModeObserve   ParticipationMode = "observe"
	ModeMention   ParticipationMode = "mention"
	ModeAssist    ParticipationMode = "assist"
	ModeProactive ParticipationMode = "proactive"
)

type ClassificationOutcome string

const (
	OutcomeSilent              ClassificationOutcome = "silent"
	OutcomeReact               ClassificationOutcome = "react"
	OutcomeReplyInThread       ClassificationOutcome = "reply_in_thread"
	OutcomeReplyInChannel      ClassificationOutcome = "reply_in_channel"
	OutcomeStartBackgroundJob  ClassificationOutcome = "start_background_job"
	OutcomeEscalateForApproval ClassificationOutcome = "escalate_for_approval"
)

type DisclosureClass string

const (
	DisclosureDestinationSafe     DisclosureClass = "destination_safe"
	DisclosureRestrictedAwareness DisclosureClass = "restricted_awareness_only"
)

type ClassificationDecision struct {
	Outcome                   ClassificationOutcome `json:"outcome"`
	Confidence                float64               `json:"confidence"`
	ReasonCodes               []string              `json:"reason_codes"`
	TopicIDs                  []string              `json:"topic_ids,omitempty"`
	ReleasableEvidenceIDs     []string              `json:"releasable_evidence_ids,omitempty"`
	RestrictedSignalIDs       []string              `json:"restricted_signal_ids,omitempty"`
	ResponseIntent            string                `json:"response_intent,omitempty"`
	DirectReply               string                `json:"direct_reply,omitempty"`
	SourceWriteRequested      bool                  `json:"source_write_requested"`
	ProductRetrievalRequired  bool                  `json:"authoritative_product_retrieval_required"`
	DisclosureClass           DisclosureClass       `json:"disclosure_class"`
	RequiresFullAgent         bool                  `json:"requires_full_agent"`
	Reaction                  string                `json:"reaction,omitempty"`
	AgentModelProfile         string                `json:"agent_model_profile,omitempty"`
	AgentModelStrength        string                `json:"agent_model_strength,omitempty"`
	AgentReasoningEffort      string                `json:"agent_reasoning_effort,omitempty"`
	ClassifierModel           string                `json:"classifier_model,omitempty"`
	ClassifierReasoningEffort string                `json:"classifier_reasoning_effort,omitempty"`
	ClassifierResponseID      string                `json:"classifier_response_id,omitempty"`
	ClassifierInputTokens     int64                 `json:"classifier_input_tokens,omitempty"`
	ClassifierOutputTokens    int64                 `json:"classifier_output_tokens,omitempty"`
}
