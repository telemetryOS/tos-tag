package harness

import (
	"context"
	"testing"
)

func TestFakeHarnessLifecycle(t *testing.T) {
	fake := NewFake()
	session, err := fake.CreateSession(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	prompt := Prompt{Text: "hello", Model: "fake/model", RequestID: "request-1", SlackFormat: "slack-output/v3"}
	if err := fake.Prompt(context.Background(), session.ID, prompt); err != nil {
		t.Fatal(err)
	}
	events, errs := fake.Events(context.Background(), session.ID)
	var count int
	for range events {
		count++
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(fake.Prompts(session.ID)) != 1 {
		t.Fatalf("events=%d prompts=%d", count, len(fake.Prompts(session.ID)))
	}
	if err := fake.Abort(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
}
