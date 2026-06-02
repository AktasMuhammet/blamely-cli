package daemon

// InvalidateSessionCache drops cached branch/HEAD lookups for repo after a
// commit so the next edit resolves to a fresh work session (new HEAD).
func InvalidateSessionCache(repo string) {
	if repo == "" {
		return
	}
	sessions.mu.Lock()
	delete(sessions.cache, repo)
	sessions.mu.Unlock()
}
