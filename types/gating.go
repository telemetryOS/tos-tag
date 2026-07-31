package types

type ParticipationMode string

const (
	ModeObserve   ParticipationMode = "observe"
	ModeMention   ParticipationMode = "mention"
	ModeAssist    ParticipationMode = "assist"
	ModeProactive ParticipationMode = "proactive"
)

type GatingOutcome string

const (
	OutcomeSilent              GatingOutcome = "silent"
	OutcomeReact               GatingOutcome = "react"
	OutcomeReplyInThread       GatingOutcome = "reply_in_thread"
	OutcomeReplyInChannel      GatingOutcome = "reply_in_channel"
	OutcomeStartBackgroundJob  GatingOutcome = "start_background_job"
	OutcomeEscalateForApproval GatingOutcome = "escalate_for_approval"
)

type DisclosureClass string

const (
	DisclosureDestinationSafe     DisclosureClass = "destination_safe"
	DisclosureRestrictedAwareness DisclosureClass = "restricted_awareness_only"
)

type ChatGatingDecision struct {
	Outcome               GatingOutcome   `json:"outcome"`
	Confidence            float64         `json:"confidence"`
	ReasonCodes           []string        `json:"reason_codes"`
	TopicIDs              []string        `json:"topic_ids,omitempty"`
	ReleasableEvidenceIDs []string        `json:"releasable_evidence_ids,omitempty"`
	RestrictedSignalIDs   []string        `json:"restricted_signal_ids,omitempty"`
	ResponseIntent        string          `json:"response_intent,omitempty"`
	DisclosureClass       DisclosureClass `json:"disclosure_class"`
	RequiresFullAgent     bool            `json:"requires_full_agent"`
}
