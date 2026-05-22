package tools

import (
	"io"
	"log"
)

// RecordCodexFromStdin is the entry point for `blamely record codex`. Codex's
// PostToolUse hook pipes a JSON payload describing the tool invocation; the
// daemon's CodexWatcher is currently the canonical attribution path (it tails
// ~/.codex/sessions/*.jsonl). Until the hook payload schema is integrated,
// this handler drains stdin and returns nil so the hook completes cleanly
// without breaking the Codex session.
func RecordCodexFromStdin(r io.Reader) error {
	return drainAndLog(r, "codex")
}

// RecordCopilotFromStdin is implemented in copilot_record.go — it parses the
// Copilot PostToolUse payload, extracts file/line ranges, and POSTs to the
// daemon (same flow as Claude/Cursor). The stub used to live here.

// RecordGeminiFromStdin is the entry point for `blamely record gemini`.
// Gemini CLI's AfterTool hook sends a JSON payload describing the just-run
// tool. Until the Gemini payload schema is integrated, this handler drains
// stdin and returns nil so the hook completes cleanly.
func RecordGeminiFromStdin(r io.Reader) error {
	return drainAndLog(r, "gemini")
}

func drainAndLog(r io.Reader, tool string) error {
	const maxPayload = 8 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxPayload))
	if err != nil {
		// Hooks must never fail the host tool's session — log and swallow.
		log.Printf("blamely record %s: read stdin: %v", tool, err)
		return nil
	}
	if len(data) > 0 {
		log.Printf("blamely record %s: received %d bytes (handler stub)", tool, len(data))
	}
	return nil
}
