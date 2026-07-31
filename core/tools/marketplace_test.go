package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolMarketplaceLoadsSkillManifestAndReviewedScript(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "plugins", "linear")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"catalog.json":             `{"id":"tools","version":"v1","tools":[{"name":"linear","path":"plugins/linear"}]}`,
		"plugins/linear/SKILL.md":  "---\nname: linear\n---\nUse the reviewed Linear helper.\n",
		"plugins/linear/tool.json": `{"id":"linear","version":"1.0.0","script":"linear.sh","operations":[{"id":"read","env":["LINEAR_API_KEY"],"timeout_seconds":10,"max_output_bytes":4096,"risk":"read"}]}`,
		"plugins/linear/linear.sh": "#!/bin/sh\nprintf '%s' \"$1\"\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, relative)
		mode := os.FileMode(0o600)
		if filepath.Base(path) == "linear.sh" {
			mode = 0o700
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := LoadMarketplace(root, "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != 1 || registry.List()[0].ToolID != "linear" || registry.List()[0].ContentHash == "" {
		t.Fatalf("snapshots=%#v", registry.List())
	}
}
