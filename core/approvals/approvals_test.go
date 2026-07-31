package approvals

import (
	"testing"
	"time"
)

func TestApprovalBindsImmutableBytesAndIsSingleUse(t *testing.T) {
	store := NewStore()
	action := Action{OrganizationID: "org", ToolID: "linear", ToolVersion: "1", OperationID: "create", Arguments: map[string]any{"title": "incident"}, Destination: "linear/ENG", Risk: "write"}
	approval, err := store.Request(action, "requester", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(approval.ID, "requester"); err == nil {
		t.Fatal("self approval accepted")
	}
	if _, err := store.Approve(approval.ID, "approver"); err != nil {
		t.Fatal(err)
	}
	changed := action
	changed.Arguments = map[string]any{"title": "different"}
	if _, err := store.Consume(approval.ID, changed); err == nil {
		t.Fatal("changed action bytes were accepted")
	}
	if _, err := store.Consume(approval.ID, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(approval.ID, action); err == nil {
		t.Fatal("approval was reused")
	}
}
