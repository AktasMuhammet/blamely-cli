//go:build darwin

package install

import "testing"

func TestResolveTargetUID(t *testing.T) {
	cases := []struct {
		name string
		uid  int
		sudo string
		want int
	}{
		{"normal user, no sudo", 501, "", 501},
		{"normal user ignores SUDO_UID", 501, "999", 501},
		{"root redirects to SUDO_UID", 0, "501", 501},
		{"root without SUDO_UID stays 0", 0, "", 0},
		{"root with zero SUDO_UID stays 0", 0, "0", 0},
		{"root with non-numeric SUDO_UID stays 0", 0, "notanumber", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTargetUID(c.uid, c.sudo); got != c.want {
				t.Errorf("resolveTargetUID(%d, %q) = %d, want %d", c.uid, c.sudo, got, c.want)
			}
		})
	}
}

func TestGuiDomainTarget(t *testing.T) {
	if got, want := guiDomainTarget(501), "gui/501/com.blamely.daemon"; got != want {
		t.Errorf("guiDomainTarget(501) = %q, want %q", got, want)
	}
}
