package tools

import "github.com/blamely/blamely/internal/daemon"

// applyUsageToPayload copies model and token counts from a parsed usage block
// onto the daemon edit payload (SQLite input_tokens / output_tokens columns).
func applyUsageToPayload(p *daemon.EditPayload, u *TranscriptUsage) {
	if u == nil {
		return
	}
	if p.Model == "" && u.Model != "" {
		p.Model = u.Model
	}
	if u.InputTokens > 0 {
		p.InputTokens = int64Ptr(u.InputTokens)
	}
	if u.OutputTokens > 0 {
		p.OutputTokens = int64Ptr(u.OutputTokens)
	}
	if u.CacheReadTokens > 0 {
		p.CacheReadTokens = int64Ptr(u.CacheReadTokens)
	}
	if u.CacheWriteTokens > 0 {
		p.CacheWriteTokens = int64Ptr(u.CacheWriteTokens)
	}
}
