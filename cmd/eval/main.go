package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/evals"
)

func main() {
	live := flag.Bool("live", false, "run the naturalistic suite against the configured live OpenAI classifier")
	flag.Parse()
	var (
		score evals.Score
		err   error
	)
	if *live {
		cfg, loadErr := config.Load()
		if loadErr != nil {
			fail(loadErr)
		}
		score, err = evals.RunLive(context.Background(), *cfg)
	} else {
		score, err = evals.Run()
	}
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
	name := "eval-score.json"
	if *live {
		name = "eval-score-live.json"
	}
	path := filepath.Join(".artifacts", name)
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
