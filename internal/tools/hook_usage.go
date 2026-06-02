package tools

import "github.com/blamely/blamely/internal/daemon"

type hookUsageOptions struct {
	transcriptPath string
	sessionID      string
	tool           string // claude | cursor | copilot | codex | gemini
}

// applyHookUsage enriches a record-hook payload with model + token counts from
// the tool's on-disk session/transcript files (best-effort).
func applyHookUsage(p *daemon.EditPayload, opt hookUsageOptions) {
	if u := readHookUsage(opt); u != nil {
		applyUsageToPayload(p, u)
	}
}

func readHookUsage(opt hookUsageOptions) *TranscriptUsage {
	switch opt.tool {
	case "codex":
		if u, _ := ReadCodexSessionUsage(opt.transcriptPath); u != nil {
			return u
		}
	case "gemini":
		if u, _ := ReadGeminiTranscriptUsage(opt.transcriptPath); u != nil {
			return u
		}
	case "copilot":
		if u, _ := ReadTranscriptUsage(opt.transcriptPath); u != nil {
			return u
		}
		if path := findChatSessionPath(opt.sessionID, copilotChatSearchRoots()); path != "" {
			if u, _ := ReadChatSessionLatestUsage(path); u != nil {
				return u
			}
		}
	case "cursor":
		if u, _ := ReadTranscriptUsage(opt.transcriptPath); u != nil {
			return u
		}
		if path := findChatSessionPath(opt.sessionID, defaultCursorChatRoots()); path != "" {
			if u, _ := ReadChatSessionLatestUsage(path); u != nil {
				return u
			}
		}
	default: // claude
		if u, _ := ReadTranscriptUsage(opt.transcriptPath); u != nil {
			return u
		}
	}
	return nil
}
