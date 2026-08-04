package audit

import (
	"context"
	"strings"
	"testing"
)

func TestCanonicalHashChainAndHMACCommitment(t *testing.T) {
	chain, err := New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := chain.Append(AppendRequest{OrganizationID: "org", Type: "observation.accepted", ResourceID: "obs-1", RetentionEpoch: "2026-07", Content: []byte("sensitive message"), Metadata: map[string]any{"channel_id": "support"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := chain.Append(AppendRequest{OrganizationID: "org", Type: "decision.recorded", ResourceID: "dec-1", RetentionEpoch: "2026-07", Metadata: map[string]any{"outcome": "silent"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == "" || first.ContentCommitment == "" || second.PreviousHash != first.Hash {
		t.Fatalf("invalid chain: %#v %#v", first, second)
	}
	if strings.Contains(first.ContentCommitment, "sensitive") {
		t.Fatal("content commitment leaked plaintext")
	}
	restarted, err := New([]byte("01234567890123456789012345678901"))
	if err != nil || !restarted.VerifyContentCommitment("2026-07", []byte("sensitive message"), first.ContentCommitment) {
		t.Fatalf("commitment could not be verified after restart: %v", err)
	}
	if restarted.VerifyContentCommitment("2026-07", []byte("different message"), first.ContentCommitment) {
		t.Fatal("commitment verified different content")
	}
	if err := chain.Verify("org"); err != nil {
		t.Fatal(err)
	}

	chain.receipts["org"][0].Metadata["channel_id"] = "tampered"
	if err := chain.Verify("org"); err == nil {
		t.Fatal("tampering was not detected")
	}
}

func TestIdempotentAppendReturnsOriginalReceipt(t *testing.T) {
	appender, err := NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	request := AppendRequest{OrganizationID: "org", Type: "job.enqueued", ResourceID: "job-1", RetentionEpoch: "2026-07", IdempotencyKey: "job/job-1/enqueued"}
	first, err := appender.Append(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := appender.Append(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Sequence != 1 || second.Sequence != 1 {
		t.Fatalf("idempotent receipts first=%#v second=%#v", first, second)
	}
}

func TestAuditRejectsWeakCommitmentKey(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("weak HMAC key accepted")
	}
}

func TestAuditRejectsContentBearingMetadata(t *testing.T) {
	chain, _ := New([]byte("01234567890123456789012345678901"))
	if _, err := chain.Append(AppendRequest{OrganizationID: "org", Type: "test", RetentionEpoch: "epoch", Metadata: map[string]any{"message_text": "secret"}}); err == nil {
		t.Fatal("content-bearing audit metadata accepted")
	}
}
