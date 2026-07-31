package marketplace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBehavioralMarketplaceAndResolveTools(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills", "linear")
	if err := os.MkdirAll(skillRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: linear\n---\nUse the tool."), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := `{"id":"telemetryos-agent-skills","version":"1.0.0","skills":[{"name":"linear","path":"skills/linear","requires_tools":["linear"]}]}`
	if err := os.WriteFile(filepath.Join(root, "marketplace.json"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	_, snapshots, err := Load(root, "marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Hash == "" {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if _, err := Resolve(snapshots, map[string]bool{}); err == nil {
		t.Fatal("missing tool dependency was accepted")
	}
	if _, err := Resolve(snapshots, map[string]bool{"linear": true}); err != nil {
		t.Fatal(err)
	}
}

func TestPluginMarketplaceLoadsBehaviorWithoutInjectingScripts(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "plugins", "demo", "skills", "linear")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("# Linear"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "scripts", "linear.sh"), []byte("#!/bin/sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := `{"name":"demo-market","plugins":[{"name":"demo","source":"./plugins/demo","version":"1.0.0"}]}`
	if err := os.WriteFile(filepath.Join(root, "marketplace.json"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots, err := LoadPluginMarketplace(root, "marketplace.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || len(snapshots[0].Files) != 1 || snapshots[0].Files[0] != "SKILL.md" {
		t.Fatalf("snapshots=%#v", snapshots)
	}
}

func TestMarketplaceRejectsTraversalSymlinkAndExecutableContent(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Load(root, "../marketplace.json"); !errors.Is(err, ErrUnsafeMarketplace) {
		t.Fatalf("traversal error = %v", err)
	}
	skillRoot := filepath.Join(root, "skill")
	if err := os.MkdirAll(skillRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "run.sh"), []byte("#!/bin/sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marketplace.json"), []byte(`{"id":"m","version":"1","skills":[{"name":"bad","path":"skill"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(root, "marketplace.json"); !errors.Is(err, ErrUnsafeMarketplace) {
		t.Fatalf("executable content error = %v", err)
	}
	if err := os.Remove(filepath.Join(skillRoot, "run.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "marketplace.json"), filepath.Join(skillRoot, "reference.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := Load(root, "marketplace.json"); !errors.Is(err, ErrUnsafeMarketplace) {
		t.Fatalf("symlink error = %v", err)
	}
}
