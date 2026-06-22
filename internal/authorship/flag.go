package authorship

// Enabled reports whether the Attribution v2 working-log engine is active (capture,
// note flip, seeding, GC, deletions). As of the v2-only beta it is ALWAYS on — the
// legacy content_sha guesser has been retired, so there is no opt-out. The function
// is retained (returning a constant) so the many gate call sites keep compiling; they
// can be inlined in a later cleanup.
func Enabled() bool {
	return true
}
