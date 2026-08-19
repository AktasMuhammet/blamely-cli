package tools

import (
	"os"
	"testing"
)

// TestMain silences the Claude hook debug log for the whole package.
//
// Several tests here drive RecordClaudeFromStdin directly without pinning HOME
// (claude_test.go), and the hook logger writes to ~/.blamely/claude-debug.log —
// so without this the suite would append test payloads to the DEVELOPER's real
// log, which is exactly the file they run `blamely log claude --debug` to read.
// Tests that assert on the logger opt back in with t.Setenv (see debugHome).
func TestMain(m *testing.M) {
	os.Setenv(claudeDebugEnvVar, "0")
	os.Exit(m.Run())
}
