package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telemetryos/tos-tag/evals"
)

func main() {
	score, err := evals.Run()
	if err != nil {
		fail(err)
	}
	data, err := json.MarshalIndent(score, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(".artifacts", 0o750); err != nil {
		fail(err)
	}
	path := filepath.Join(".artifacts", "eval-score.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		fail(err)
	}
	_, _ = fmt.Printf("%s\n", data)
	if !score.ThresholdSatisfied {
		os.Exit(1)
	}
}

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "tos-tag-eval: %v\n", err)
	os.Exit(1)
}
