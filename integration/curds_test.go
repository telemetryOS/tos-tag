package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurdsReviewedHelperUsesOpenAIDefaultModelAndServerArtifact(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "arguments")
	artifact := filepath.Join(temporary, "generated.webp")
	fake := filepath.Join(temporary, "curds")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_PATH\"\nprintf 'RIFF\\000\\000\\000\\000WEBPVP8 ' > \"$TOS_TAG_ARTIFACT_PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "curds", "run.sh")
	command := exec.Command("/bin/bash", helper, "a puppy in soft natural light", "1:1", "auto")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"TMPDIR=" + temporary,
		"OPENAI_API_KEY=test-only",
		"TOS_TAG_OPERATION_ID=generate",
		"TOS_TAG_ARTIFACT_PATH=" + artifact,
		"CAPTURE_PATH=" + capture,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reviewed helper failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(arguments)
	for _, required := range []string{"-no-tui", "-provider\nopenai", "-number-of-images\n1", "-output-format\nwebp", "-output\n" + artifact} {
		if !strings.Contains(text, required) {
			t.Fatalf("reviewed Curds arguments missing %q: %s", required, text)
		}
	}
	if strings.Contains(text, "-model") || strings.Contains(text, "test-only") {
		t.Fatalf("reviewed Curds arguments exposed an invalid model override or token: %s", text)
	}
	if info, err := os.Stat(artifact); err != nil || info.Size() == 0 {
		t.Fatalf("artifact info=%#v err=%v", info, err)
	}
}
