package evals

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/types"
)

func TestBehavioralEval(t *testing.T) {
	score, err := Run()
	if err != nil {
		t.Fatal(err)
	}
	if !score.ThresholdSatisfied {
		encoded, _ := json.MarshalIndent(score, "", "  ")
		t.Fatalf("behavioral eval threshold failed:\n%s", encoded)
	}
}

func TestNaturalisticFixturesRejectEvaluatorCues(t *testing.T) {
	for _, cue := range []string{
		"no response needed",
		"please reply in the channel",
		"use a thread for this",
		"this is a classifier probe",
		"include a clickable link to the Agent Wiki page you used",
		"cite your sources",
	} {
		fixture := Fixture{Name: "contaminated", Target: classifier.Target{Envelope: types.SlackEnvelope{Text: cue}}}
		if err := validateNaturalisticFixture(fixture); err == nil || !strings.Contains(err.Error(), "evaluator cue") {
			t.Fatalf("cue %q was not rejected: %v", cue, err)
		}
	}
}

func TestBehavioralFixturesAreNaturalistic(t *testing.T) {
	for _, fixture := range Fixtures() {
		if err := validateNaturalisticFixture(fixture); err != nil {
			t.Fatal(err)
		}
		providerInput, err := json.Marshal(struct {
			Target classifier.Target         `json:"target"`
			Pack   types.ContextPackRevision `json:"context_pack"`
		}{Target: fixture.Target, Pack: fixture.Pack})
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(providerInput)
		for _, evaluatorOnly := range []string{
			fixture.Name,
			"want_predicted",
			"want_effective",
			"live_predicted",
			"live_effective",
			"want_live_reactions",
			"want_live_routes",
		} {
			if strings.Contains(encoded, evaluatorOnly) {
				t.Fatalf("fixture %q leaked evaluator-only value %q into provider input", fixture.Name, evaluatorOnly)
			}
		}
	}
}

func TestBehavioralEvalIncludesExpandedNaturalisticMatrix(t *testing.T) {
	if got := len(Fixtures()); got != 52 {
		t.Fatalf("behavioral classifier fixtures=%d, want 52 plus two infrastructure invariants", got)
	}
	score, err := Run()
	if err != nil {
		t.Fatal(err)
	}
	if score.Total != 54 {
		t.Fatalf("behavioral eval total=%d, want 54", score.Total)
	}
}
