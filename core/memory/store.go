// Package memory owns durable, source-linked summaries and facts used to build
// future context packs. The control plane, not a model session, is authoritative.
package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusForgotten Status = "forgotten"
)

type Scope string

const (
	ScopeChannel Scope = "channel"
	ScopeThread  Scope = "thread"
)

type Fact struct {
	Text       string    `json:"text" bson:"text"`
	Confidence float64   `json:"confidence" bson:"confidence"`
	SourceIDs  []string  `json:"source_ids" bson:"source_ids"`
	ExpiresAt  time.Time `json:"expires_at" bson:"expires_at"`
}

type Record struct {
	ID               string    `json:"id" bson:"public_id"`
	OrganizationID   string    `json:"organization_id" bson:"organization_id"`
	ChannelID        string    `json:"channel_id" bson:"channel_id"`
	RootThreadTS     string    `json:"root_thread_ts,omitempty" bson:"root_thread_ts,omitempty"`
	Scope            Scope     `json:"scope" bson:"scope"`
	ScopeKey         string    `json:"-" bson:"scope_key"`
	Restricted       bool      `json:"restricted" bson:"restricted"`
	Text             string    `json:"text,omitempty" bson:"text,omitempty"`
	Facts            []Fact    `json:"facts,omitempty" bson:"facts,omitempty"`
	Confidence       float64   `json:"confidence" bson:"confidence"`
	SourceIDs        []string  `json:"source_ids,omitempty" bson:"source_ids,omitempty"`
	SourceHash       string    `json:"-" bson:"source_hash"`
	Origin           string    `json:"origin" bson:"origin"`
	Model            string    `json:"model,omitempty" bson:"model,omitempty"`
	ReasoningEffort  string    `json:"reasoning_effort,omitempty" bson:"reasoning_effort,omitempty"`
	Pinned           bool      `json:"pinned" bson:"pinned"`
	Status           Status    `json:"status" bson:"status"`
	Revision         int64     `json:"revision" bson:"revision"`
	CreatedAt        time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" bson:"updated_at"`
	NaturalExpiresAt time.Time `json:"-" bson:"natural_expires_at,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty" bson:"expires_at,omitempty"`
}

type Repository interface {
	List(context.Context, string, int) ([]Record, error)
	Recall(context.Context, string, string, string, time.Time, int) ([]Record, error)
	FindScope(context.Context, string, string) (Record, error)
	PutGenerated(context.Context, Record) (Record, bool, error)
	Correct(context.Context, string, string, string, string) (Record, error)
	SetPinned(context.Context, string, string, bool, string) (Record, error)
	Forget(context.Context, string, string, string) (Record, error)
}

var ErrNotFound = errors.New("memory record not found")

func validateText(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 12_000 {
		return "", errors.New("memory text must contain 1 to 12000 characters")
	}
	return value, nil
}
