package types

import "time"

type ContextPartition string

const (
	PartitionSystem    ContextPartition = "system"
	PartitionThread    ContextPartition = "thread"
	PartitionChannel   ContextPartition = "channel"
	PartitionRecentOrg ContextPartition = "recent_org"
	PartitionEvidence  ContextPartition = "evidence"
	PartitionSituation ContextPartition = "situation"
)

type ContextCandidate struct {
	ID              string           `json:"id"`
	Version         int64            `json:"version"`
	OrganizationID  string           `json:"organization_id"`
	ChannelID       string           `json:"channel_id,omitempty"`
	ChannelName     string           `json:"channel_name,omitempty"`
	AuthorID        string           `json:"author_id,omitempty"`
	Partition       ContextPartition `json:"partition"`
	Provenance      string           `json:"provenance"`
	Text            string           `json:"text"`
	Priority        int              `json:"priority"`
	ObservedAt      time.Time        `json:"observed_at"`
	DisclosureClass DisclosureClass  `json:"disclosure_class"`
	Required        bool             `json:"required"`
	SourceExpiresAt time.Time        `json:"source_expires_at,omitempty"`
}

type ContextSource struct {
	ID              string           `json:"id"`
	Version         int64            `json:"version"`
	ChannelID       string           `json:"channel_id,omitempty"`
	ChannelName     string           `json:"channel_name,omitempty"`
	AuthorID        string           `json:"author_id,omitempty"`
	Partition       ContextPartition `json:"partition"`
	Provenance      string           `json:"provenance"`
	TokenCount      int              `json:"token_count"`
	DisclosureClass DisclosureClass  `json:"disclosure_class"`
	Text            string           `json:"text"`
	ObservedAt      time.Time        `json:"observed_at,omitempty"`
}

type ContextPackRevision struct {
	ID                    RevisionID               `json:"id"`
	OrganizationID        string                   `json:"organization_id"`
	TargetObservationID   string                   `json:"target_observation_id"`
	OrganizationWatermark int64                    `json:"organization_watermark"`
	PolicyRevision        string                   `json:"policy_revision"`
	MembershipRevision    string                   `json:"membership_revision"`
	TokenizerRevision     string                   `json:"tokenizer_revision"`
	Sources               []ContextSource          `json:"sources"`
	PartitionTokens       map[ContextPartition]int `json:"partition_tokens"`
	TotalTokens           int                      `json:"total_tokens"`
	ContentHash           string                   `json:"content_hash"`
	CreatedAt             time.Time                `json:"created_at"`
	ExpiresAt             time.Time                `json:"expires_at"`
}
