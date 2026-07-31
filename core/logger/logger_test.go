package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/config"
)

func TestStructuredLogFileIsOwnerReadableAndIncludesContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	cfg := config.DefaultConfiguration
	cfg.Logging.Level = "debug"
	cfg.Logging.FilePath = path
	lgr := New(&cfg)
	lgr.WithCtx(blackbox.Ctx{"event_id": "event-1"}).Info("event persisted")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "event persisted") || !strings.Contains(string(data), "event-1") {
		t.Fatalf("structured log missing lifecycle context: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %o, want 600", info.Mode().Perm())
	}
}
