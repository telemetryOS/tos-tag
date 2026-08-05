package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigitalOceanReviewedHelperMatchesCanonicalSource(t *testing.T) {
	canonicalPath := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills", "src", "skills", "digitalocean", "scripts", "digitalocean.sh"))
	reviewedPath := filepath.Join("..", "tool-marketplace", "tools", "digitalocean", "run.sh")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := os.ReadFile(reviewedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(reviewed) {
		t.Fatal("reviewed DigitalOcean helper differs from canonical skill source")
	}
}

func TestDigitalOceanReadUsesIsolatedStateAndEnvironmentToken(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "doctl-capture.txt")
	fakeDoctl := filepath.Join(temporary, "doctl")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" >\"$CAPTURE_PATH\"\n" +
		"printf 'HOME=%s\\nXDG=%s\\nTOKEN=%s\\n' \"$HOME\" \"$XDG_CONFIG_HOME\" \"$DIGITALOCEAN_ACCESS_TOKEN\" >>\"$CAPTURE_PATH\"\n" +
		"printf '{\"account\":{\"status\":\"active\"}}\\n'\n"
	if err := os.WriteFile(fakeDoctl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	key := "dop_v1_fixture_digitalocean_token_0123456789"
	helper := filepath.Join("..", "tool-marketplace", "tools", "digitalocean", "run.sh")
	command := exec.Command("bash", helper, "account")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary + "/caller-home",
		"DIGITAL_OCEAN_API_KEY=" + key,
		"TOS_TAG_OPERATION_ID=read",
		"CAPTURE_PATH=" + capture,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("DigitalOcean read failed: %v: %s", err, output)
	}
	if !strings.Contains(string(output), `"status": "active"`) || strings.Contains(string(output), key) {
		t.Fatalf("unexpected or secret-bearing output: %s", output)
	}
	captured, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(captured)
	if !strings.Contains(text, "--output\njson\n--http-retry-max\n2\naccount\nget\n") {
		t.Fatalf("unexpected doctl argv: %s", text)
	}
	if strings.Contains(text, "--access-token") || !strings.Contains(text, "TOKEN="+key) {
		t.Fatalf("token was not isolated to environment: %s", text)
	}
	if strings.Contains(text, "HOME="+temporary+"/caller-home") || !strings.Contains(text, "tos-tag-digitalocean-home.") {
		t.Fatalf("doctl state was not isolated: %s", text)
	}
}

func TestDigitalOceanMutationRoutingIsExact(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "doctl-argv.txt")
	fakeDoctl := filepath.Join(temporary, "doctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$CAPTURE_PATH\"\nprintf '{}\\n'\n"
	if err := os.WriteFile(fakeDoctl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "digitalocean", "run.sh")
	command := exec.Command("bash", helper, "delete-cluster", "cluster-1")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"DIGITAL_OCEAN_API_KEY=dop_v1_fixture_digitalocean_token_0123456789",
		"TOS_TAG_OPERATION_ID=delete",
		"CAPTURE_PATH=" + capture,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cluster delete failed: %v: %s", err, output)
	}
	argv, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(argv)
	for _, expected := range []string{"kubernetes\ncluster\ndelete\ncluster-1\n", "--force\n", "--update-kubeconfig=false\n"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("delete argv missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "--dangerous") {
		t.Fatalf("cascading deletion leaked into argv: %s", text)
	}
}

func TestDigitalOceanHelperRejectsUnreviewedCommandsAndArguments(t *testing.T) {
	helper := filepath.Join("..", "tool-marketplace", "tools", "digitalocean", "run.sh")
	tests := []struct {
		name      string
		operation string
		arguments []string
		want      string
	}{
		{name: "raw command", operation: "read", arguments: []string{"auth", "init"}, want: "not permitted"},
		{name: "wrong risk", operation: "read", arguments: []string{"delete-droplet", "123"}, want: "not permitted"},
		{name: "arbitrary flag", operation: "read", arguments: []string{"droplet", "--access-token"}, want: "malformed"},
		{name: "too many targets", operation: "delete", arguments: []string{"delete-droplet", "123", "456"}, want: "exactly 1"},
		{name: "path-like target", operation: "delete", arguments: []string{"delete-app", "../secret"}, want: "malformed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", append([]string{helper}, test.arguments...)...)
			command.Env = []string{
				"PATH=/usr/bin:/bin",
				"HOME=" + t.TempDir(),
				"DIGITAL_OCEAN_API_KEY=dop_v1_fixture_digitalocean_token_0123456789",
				"TOS_TAG_OPERATION_ID=" + test.operation,
			}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("expected rejection containing %q, err=%v output=%s", test.want, err, output)
			}
		})
	}
}

func TestDigitalOceanProviderFailureDoesNotExposeTokenOrStderr(t *testing.T) {
	temporary := t.TempDir()
	fakeDoctl := filepath.Join(temporary, "doctl")
	key := "dop_v1_fixture_digitalocean_token_0123456789"
	script := "#!/bin/sh\nprintf 'provider rejected " + key + "' >&2\nexit 1\n"
	if err := os.WriteFile(fakeDoctl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "digitalocean", "run.sh")
	command := exec.Command("bash", helper, "account")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"DIGITAL_OCEAN_API_KEY=" + key,
		"TOS_TAG_OPERATION_ID=read",
	}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "CLI request failed with exit status 1") {
		t.Fatalf("expected bounded provider failure, err=%v output=%s", err, output)
	}
	if strings.Contains(string(output), key) || strings.Contains(string(output), "provider rejected") {
		t.Fatalf("provider failure exposed stderr or token: %s", output)
	}
}
