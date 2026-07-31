package types

type ModelProfile struct {
	ID                   string         `json:"id" bson:"id"`
	ProviderID           string         `json:"provider_id" bson:"provider_id"`
	ModelID              string         `json:"model_id" bson:"model_id"`
	Variant              string         `json:"variant,omitempty" bson:"variant,omitempty"`
	ProviderOptions      map[string]any `json:"provider_options,omitempty" bson:"provider_options,omitempty"`
	RequiredCapabilities []string       `json:"required_capabilities,omitempty" bson:"required_capabilities,omitempty"`
	AllowedDataClasses   []string       `json:"allowed_data_classes,omitempty" bson:"allowed_data_classes,omitempty"`
	FallbackProfileIDs   []string       `json:"fallback_profile_ids,omitempty" bson:"fallback_profile_ids,omitempty"`
	MaxInputTokens       int            `json:"max_input_tokens" bson:"max_input_tokens"`
	MaxOutputTokens      int            `json:"max_output_tokens" bson:"max_output_tokens"`
	Enabled              bool           `json:"enabled" bson:"enabled"`
}

type ModelRouteContext struct {
	OrganizationID string   `json:"organization_id"`
	WorkspaceID    string   `json:"workspace_id,omitempty"`
	ChannelID      string   `json:"channel_id,omitempty"`
	RoutineID      string   `json:"routine_id,omitempty"`
	SkillName      string   `json:"skill_name,omitempty"`
	Phase          string   `json:"phase,omitempty"`
	Override       string   `json:"override,omitempty"`
	ChannelDefault string   `json:"channel_default,omitempty"`
	DataClasses    []string `json:"data_classes,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	InputTokens    int      `json:"input_tokens,omitempty"`
}

type ResolvedModel struct {
	ProfileID  string `json:"profile_id"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Variant    string `json:"variant,omitempty"`
	PolicyRev  string `json:"policy_revision"`
}

type DecisionTrace struct {
	MatchedRule string   `json:"matched_rule"`
	Tried       []string `json:"tried"`
	Rejected    []string `json:"rejected,omitempty"`
	Fallback    bool     `json:"fallback"`
}
