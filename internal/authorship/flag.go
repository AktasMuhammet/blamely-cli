package authorship

import (
	"os"
	"strings"
)

// Enabled reports whether the Attribution v2 working-log engine is active (capture,
// note flip, seeding, GC). ON by default; opt out with BLAMELY_ATTRIBUTION_V2 set to
// 0/false/off/no/disable(d). When on, the committed note is sourced from the
// diff-based working log, falling back to the legacy engine for any file that has no
// working log — so it degrades safely where v2 never captured.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BLAMELY_ATTRIBUTION_V2"))) {
	case "0", "false", "off", "no", "disable", "disabled":
		return false
	}
	return true
}
