package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripeReviewedHelperMatchesCanonicalSource(t *testing.T) {
	canonicalPath := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills", "src", "skills", "stripe", "scripts", "stripe.sh"))
	reviewedPath := filepath.Join("..", "tool-marketplace", "tools", "stripe", "run.sh")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := os.ReadFile(reviewedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, reviewed) {
		t.Fatal("reviewed Stripe helper drifted from the canonical skill source")
	}
}

func TestStripeReviewedHelperRoutesAndProtectsKey(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "stripe-argv.txt")
	fakeStripe := filepath.Join(temporary, "stripe")
	const fakeStripeScript = `#!/bin/bash
set -eu
printf 'HOME=%s\n' "$HOME" > "$CAPTURE_PATH"
printf '%s\n' "$@" >> "$CAPTURE_PATH"
printf '%s' '{"object":"list","data":[{"id":"cus_fixture"}]}'
`
	if err := os.WriteFile(fakeStripe, []byte(fakeStripeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "stripe", "run.sh")
	key := "rk_live_fixture_stripe_key"
	command := exec.Command("/bin/bash", helper, "get", "/v1/customers", "--limit", "10", "--data", "email=person@example.test", "--expand", "data.subscriptions")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"TMPDIR=" + temporary,
		"STRIPE_API_KEY=" + key,
		"TOS_TAG_OPERATION_ID=read",
		"CAPTURE_PATH=" + capture,
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "cus_fixture") {
		t.Fatalf("Stripe read failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"get\n/v1/customers",
		"--limit\n10",
		"--data\nemail=person@example.test",
		"--expand\ndata.subscriptions",
		"--live",
		"HOME=" + temporary + "/tos-tag-stripe-home.",
	} {
		if !strings.Contains(string(arguments), expected) {
			t.Fatalf("Stripe arguments missing %q: %s", expected, arguments)
		}
	}
	if bytes.Contains(arguments, []byte(key)) || bytes.Contains(output, []byte(key)) {
		t.Fatal("Stripe key escaped into argv or output")
	}
}

func TestStripeReviewedHelperConfirmsIdempotentWrite(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "stripe-argv.txt")
	fakeStripe := filepath.Join(temporary, "stripe")
	const fakeStripeScript = `#!/bin/bash
set -eu
printf '%s\n' "$@" > "$CAPTURE_PATH"
printf '%s' '{"id":"cus_fixture","object":"customer"}'
`
	if err := os.WriteFile(fakeStripe, []byte(fakeStripeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "stripe", "run.sh")
	command := exec.Command("/bin/bash", helper, "post", "/v1/customers/cus_fixture", "--idempotency", "tag-request-123", "--data", "metadata[reviewed]=true")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "STRIPE_API_KEY=rk_live_fixture_stripe_key", "TOS_TAG_OPERATION_ID=write", "CAPTURE_PATH=" + capture}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "cus_fixture") {
		t.Fatalf("Stripe write failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"post\n/v1/customers/cus_fixture", "--idempotency\ntag-request-123", "--confirm", "--live"} {
		if !strings.Contains(string(arguments), expected) {
			t.Fatalf("Stripe write arguments missing %q: %s", expected, arguments)
		}
	}
}

func TestStripeReviewedHelperEnforcesRiskAndArgumentCatalog(t *testing.T) {
	helper := filepath.Join("..", "tool-marketplace", "tools", "stripe", "run.sh")
	tests := []struct {
		name      string
		operation string
		arguments []string
		want      string
	}{
		{name: "read cannot write", operation: "read", arguments: []string{"post", "/v1/customers", "--idempotency", "tag-request-123"}, want: "not permitted"},
		{name: "write cannot read", operation: "write", arguments: []string{"get", "/v1/account"}, want: "not permitted"},
		{name: "delete cannot post", operation: "delete", arguments: []string{"post", "/v1/refunds", "--idempotency", "tag-request-123"}, want: "not permitted"},
		{name: "reject arbitrary origin", operation: "read", arguments: []string{"get", "https://api.stripe.com/v1/account"}, want: "documented /v1 or /v2"},
		{name: "reject query in path", operation: "read", arguments: []string{"get", "/v1/customers?limit=100"}, want: "must not contain query"},
		{name: "reject arbitrary flag", operation: "read", arguments: []string{"get", "/v1/account", "--api-key", "private"}, want: "unsupported argument"},
		{name: "reject live flag", operation: "read", arguments: []string{"get", "/v1/account", "--live"}, want: "unsupported argument"},
		{name: "reject test key", operation: "read", arguments: []string{"get", "/v1/account"}, want: "live-mode secret or restricted key"},
		{name: "write requires idempotency", operation: "write", arguments: []string{"post", "/v1/customers", "--data", "name=Fixture"}, want: "requires --idempotency"},
		{name: "delete requires idempotency", operation: "delete", arguments: []string{"delete", "/v1/customers/cus_fixture"}, want: "requires --idempotency"},
		{name: "reject invalid data", operation: "read", arguments: []string{"get", "/v1/customers", "--data", "not-a-field"}, want: "form-field=value"},
		{name: "reject write pagination", operation: "write", arguments: []string{"post", "/v1/customers", "--limit", "10", "--idempotency", "tag-request-123"}, want: "read-only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("/bin/bash", append([]string{helper}, test.arguments...)...)
			key := "rk_live_fixture_stripe_key"
			if test.name == "reject test key" {
				key = "rk_test_fixture_stripe_key"
			}
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "STRIPE_API_KEY=" + key, "TOS_TAG_OPERATION_ID=" + test.operation}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("error=%v output=%s", err, output)
			}
		})
	}
}

func TestStripeReviewedHelperRedactsProviderFailure(t *testing.T) {
	temporary := t.TempDir()
	fakeStripe := filepath.Join(temporary, "stripe")
	const fakeStripeScript = `#!/bin/bash
set -eu
printf '%s' '{"error":{"type":"invalid_request_error","code":"more_permissions_required","message":"PRIVATE-CUSTOMER-DETAIL"}}'
`
	if err := os.WriteFile(fakeStripe, []byte(fakeStripeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "stripe", "run.sh")
	command := exec.Command("/bin/bash", helper, "get", "/v1/account")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "STRIPE_API_KEY=rk_live_fixture_stripe_key", "TOS_TAG_OPERATION_ID=read"}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "more_permissions_required") || bytes.Contains(output, []byte("PRIVATE-CUSTOMER-DETAIL")) {
		t.Fatalf("error=%v output=%s", err, output)
	}
}
