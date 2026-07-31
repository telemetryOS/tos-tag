// Package types contains storage-independent boundary and API types.
package types

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type OrganizationID string
type WorkspaceID string
type ChannelID string
type ObservationID string
type MessageID string
type SessionID string
type JobID string
type AttemptID string
type DeliveryID string
type ReceiptID string
type RevisionID string
type PrincipalID string
type WorkerID string

func NewID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		panic("empty ID prefix")
	}
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func ValidateID(value, prefix string) error {
	if !strings.HasPrefix(value, prefix+"_") || len(value) <= len(prefix)+1 {
		return fmt.Errorf("invalid %s ID", prefix)
	}
	return nil
}
