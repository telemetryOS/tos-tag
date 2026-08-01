package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/jobs"
)

func TestProviderGatewayExchangesAttemptCapabilityForUpstreamCredential(t *testing.T) {
	var upstreamAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamAuthorization = request.Header.Get("Authorization")
		if request.URL.Path != "/v1/responses" || request.Method != http.MethodPost {
			t.Fatalf("unexpected upstream request %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	queue := jobs.NewMemoryQueue(nil)
	job, _, err := queue.Enqueue(context.Background(), jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1.0", SessionID: "session", Generation: 1, IdempotencyKey: "provider-gateway", Kind: "agent", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	job, _ = queue.Claim(context.Background(), "worker", time.Minute)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, jobs.StateRunning, nil)
	gateway, err := NewProviderGateway(ProviderGatewayOptions{ProviderID: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Timeout: time.Second, Jobs: queue})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close(context.Background())
	route, err := gateway.Register(ProviderGatewayScope{AttemptID: "attempt-1", JobID: string(job.ID), LeaseToken: job.Lease.Token, SteeringEpoch: job.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(route.Token, "upstream-secret") || strings.Contains(route.BaseURL, "upstream-secret") {
		t.Fatal("upstream credential crossed into the worker route")
	}
	request, _ := http.NewRequest(http.MethodPost, route.BaseURL+"/responses", strings.NewReader(`{"model":"test"}`))
	request.Header.Set("Authorization", "Bearer "+route.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || upstreamAuthorization != "Bearer upstream-secret" {
		t.Fatalf("status=%d upstream authorization=%q", response.StatusCode, upstreamAuthorization)
	}

	_, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, jobs.StateSucceeded, nil)
	revokedRequest, _ := http.NewRequest(http.MethodPost, route.BaseURL+"/responses", strings.NewReader(`{}`))
	revokedRequest.Header.Set("Authorization", "Bearer "+route.Token)
	revokedResponse, err := http.DefaultClient.Do(revokedRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = revokedResponse.Body.Close()
	if revokedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("capability with lost job authority status = %d", revokedResponse.StatusCode)
	}
	gateway.Revoke("attempt-1")
}

func TestProviderGatewayRejectsUnsafeConfiguration(t *testing.T) {
	for _, options := range []ProviderGatewayOptions{
		{ProviderID: "openai", BaseURL: "file:///tmp/provider", APIKey: "secret", Timeout: time.Second},
		{ProviderID: "", BaseURL: "https://api.openai.com/v1", APIKey: "secret", Timeout: time.Second},
		{ProviderID: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "", Timeout: time.Second},
	} {
		if _, err := NewProviderGateway(options); err == nil {
			t.Fatal("unsafe provider gateway configuration accepted")
		}
	}
}
