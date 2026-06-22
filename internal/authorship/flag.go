package authorship

import (
	"os"
	"strings"
)

// Enabled reports whether the Attribution v2 working-log engine runs alongside the
// existing recorder. OFF by default; set BLAMELY_ATTRIBUTION_V2=1 (or true/on/yes)
// to enable. This is the Phase 1–2 dual-run switch: while on, edits also flow into
// the working log, but the committed note and the gutter do NOT read it until the
// Phase 3 flip — so enabling it is side-effect-free for attribution output.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BLAMELY_ATTRIBUTION_V2"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}
