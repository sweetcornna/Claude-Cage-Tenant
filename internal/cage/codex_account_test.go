package cage

import (
	"strings"
	"testing"
)

func TestStartScriptInstallsCodexAccountHelperAndShim(t *testing.T) {
	script := componentStartScript()
	for _, part := range []string{
		"sudo tee /usr/local/bin/cage-codex-account >/dev/null <<'CAGE_CODEX_ACCOUNT'\n" + codexAccountHelper + "\nCAGE_CODEX_ACCOUNT\n",
		"exec /usr/local/bin/cage-codex-account run \"$CAGE_CODEX_ACCOUNT_ID\" \"$@\"",
		"sudo chmod 0755 /usr/local/lib/cage-codex-bin/codex",
	} {
		if !strings.Contains(script, part) {
			t.Fatalf("start script lacks %q", part[:min(len(part), 80)])
		}
	}
	// The helper imports client_runtime.py, so it must be installed after it.
	if strings.Index(script, "/usr/local/bin/client_runtime.py") > strings.Index(script, "/usr/local/bin/cage-codex-account") {
		t.Fatal("Codex helper installed before its runtime")
	}
	for _, line := range strings.Split(codexAccountHelper, "\n") {
		if line == "CAGE_CODEX_ACCOUNT" || line == "CAGE_CODEX_SHIM" {
			t.Fatal("helper body terminates its installer heredoc")
		}
	}
}
