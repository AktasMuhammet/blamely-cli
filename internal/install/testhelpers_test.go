package install

import "testing"

// fakeHomeDir points os.UserHomeDir at a fresh temp directory for the duration
// of the test. Sets HOME (POSIX), USERPROFILE (Windows), and clears
// HOMEDRIVE/HOMEPATH so the Windows fallback can't sneak in.
func fakeHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	return home
}
