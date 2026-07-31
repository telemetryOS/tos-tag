package marketplace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadPluginSelectsOnePluginAndAllowsAnEmptyBase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "plugins", "headless", "skills", "queue"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plugins", "base", "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugins", "headless", "skills", "queue", "SKILL.md"), []byte("# Queue"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := `{"name":"agents","plugins":[{"name":"headless","source":"./plugins/headless","version":"1.0.0"},{"name":"base","source":"./plugins/base","version":"0.1.0"}]}`
	if err := os.WriteFile(filepath.Join(root, "marketplace.json"), []byte(catalog), 0o600); err != nil {
		t.Fatal(err)
	}
	headless, err := LoadPlugin(root, "marketplace.json", "headless")
	if err != nil || len(headless) != 1 || headless[0].Name != "queue" || headless[0].MarketplaceID != "agents/headless" {
		t.Fatalf("headless=%#v err=%v", headless, err)
	}
	base, err := LoadPlugin(root, "marketplace.json", "base")
	if err != nil || len(base) != 0 {
		t.Fatalf("base=%#v err=%v", base, err)
	}
	if _, err := LoadPlugin(root, "marketplace.json", "missing"); err == nil {
		t.Fatal("missing selected plugin was accepted")
	}
}

func TestLoadPluginSnapshotsSharedReferencesButExcludesSharedScripts(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "plugins", "headless", "skills")
	for _, directory := range []string{
		filepath.Join(skillsRoot, "queue"),
		filepath.Join(skillsRoot, ".references"),
		filepath.Join(skillsRoot, ".scripts"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "queue", "SKILL.md"), []byte("Read ../.references/common.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, ".references", "common.md"), []byte("# Common\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, ".scripts", "unsafe.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marketplace.json"), []byte(`{"name":"agents","plugins":[{"name":"headless","source":"./plugins/headless","version":"1.0.0"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots, err := LoadPlugin(root, "marketplace.json", "headless")
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
	snapshot := snapshots[0]
	if snapshot.SharedRoot != skillsRoot || len(snapshot.SharedFiles) != 1 || snapshot.SharedFiles[0] != ".references/common.md" || snapshot.SharedHash == "" {
		t.Fatalf("shared snapshot=%#v", snapshot)
	}
	for _, path := range append(append([]string(nil), snapshot.Files...), snapshot.SharedFiles...) {
		if strings.Contains(path, ".scripts") || strings.HasSuffix(path, ".sh") {
			t.Fatalf("script entered behavioral snapshot: %s", path)
		}
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
