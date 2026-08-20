package install

import (
	"strings"
	"testing"
)

// A PreToolUse registration MUST carry --pre. Without it the pre-hook runs the
// full recording path against the file's PRE-edit content, so the agent is
// credited with lines that were there before it wrote anything — the user's own
// work included. This is the regression that made PreToolUse look unusable.
func TestRecordHookCommandForEvent_PreEvents(t *testing.T) {
	const bin = "/home/u/.blamely/bin/blamely"

	for _, event := range []string{
		"PreToolUse",   // Claude, Copilot
		"pre_tool_use", // Codex
		"BeforeTool",   // Gemini
		"pretooluse",   // case-insensitive
	} {
		got := recordHookCommandForEvent(bin, "claude", event)
		if !strings.HasSuffix(got, " --pre") {
			t.Errorf("event %q → %q, want a --pre suffix", event, got)
		}
	}

	for _, event := range []string{"PostToolUse", "post_tool_use", "AfterTool", "postToolUse"} {
		got := recordHookCommandForEvent(bin, "claude", event)
		if strings.Contains(got, "--pre") {
			t.Errorf("event %q → %q, want no --pre", event, got)
		}
	}
}

// The " record <tool>" marker is what uninstall and the dedupe-on-reinstall scan
// match on, so appending --pre must not break it.
func TestRecordHookCommandForEvent_KeepsMarker(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "copilot", "gemini", "cursor"} {
		got := recordHookCommandForEvent("/home/u/.blamely/bin/blamely", tool, "PreToolUse")
		if !strings.Contains(got, "record "+tool) {
			t.Errorf("%q lost the %q marker", got, "record "+tool)
		}
	}
	// A path with a space stays quoted, and the flag lands outside the quotes.
	got := recordHookCommandForEvent(`C:\Users\First Last\.blamely\bin\blamely.exe`, "claude", "PreToolUse")
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `" record claude --pre`) {
		t.Errorf("quoted path: got %q", got)
	}
}

// Every agent we register a pre-edit event for must spell that event in a way
// isPreEditHookEvent recognises — otherwise it silently gets the post-hook
// command and starts mis-attributing.
func TestHookEventLists_PreEventsAreRecognised(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		events []string
	}{
		{"claude", claudeHookEvents},
		{"codex", codexHookEvents},
		{"copilot", copilotHookEvents},
		{"gemini", geminiHookEvents},
		{"cursor", cursorHookEvents},
	} {
		var pre int
		for _, e := range tc.events {
			low := strings.ToLower(e)
			looksPre := strings.HasPrefix(low, "pre") || strings.HasPrefix(low, "before")
			if looksPre && !isPreEditHookEvent(e) {
				t.Errorf("%s: event %q looks pre-edit but isPreEditHookEvent says no", tc.tool, e)
			}
			if isPreEditHookEvent(e) {
				pre++
			}
		}
		t.Logf("%s: %d pre-edit event(s) of %v", tc.tool, pre, tc.events)
	}
}
