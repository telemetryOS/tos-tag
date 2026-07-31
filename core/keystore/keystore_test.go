package keystore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValuesAreEncryptedAndNeverListed(t *testing.T) {
	store, err := New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := store.Put(context.Background(), "org", "LINEAR_API_KEY", "linear tool", "super-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.List(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(values)
	if strings.Contains(string(listed), "super-secret-value") {
		t.Fatal("secret value was listed")
	}
	stored := store.values[reference.ID]
	if strings.Contains(string(stored.Ciphertext), "super-secret-value") {
		t.Fatal("secret value was not encrypted")
	}
	resolved, err := store.Resolve(context.Background(), "org", reference.ID)
	if err != nil || resolved != "super-secret-value" {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	if _, err := store.Resolve(context.Background(), "other-org", reference.ID); err == nil {
		t.Fatal("cross-tenant secret access was accepted")
	}
}
