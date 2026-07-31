package evals

import (
	"encoding/json"
	"testing"
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
