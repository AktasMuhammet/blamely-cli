package config

import (
	"os"
	"testing"
)

// TestUpdateDefaults pins the shipped policy: blamely keeps itself current on
// its own, and a site that needs to pin a version opts out.
func TestUpdateDefaults(t *testing.T) {
	fakeHome(t)
	cfg := LoadConfig()
	if !cfg.Update.Check {
		t.Error("update.check must default to on")
	}
	if !cfg.Update.Auto {
		t.Error("update.auto must default to ON — a stale build mis-attributes silently")
	}
	if got := cfg.UpdateIntervalHours(); got != DefaultUpdateIntervalHours {
		t.Errorf("interval = %d, want the %d-hour default", got, DefaultUpdateIntervalHours)
	}
}

// Turning the check off must also stop updates being applied: runUpdateCheck
// bails before it ever asks, so check=false is a complete kill switch and not
// just "install silently without recording a hint".
func TestUpdateCheckOffIsACompleteKillSwitch(t *testing.T) {
	fakeHome(t)
	cfg := DefaultConfig()
	cfg.Update.Check = false
	if _, err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig()
	if got.Update.Check {
		t.Fatal("check did not persist as off")
	}
	// Auto stays true in the file; the daemon's gate is what makes it moot.
	if !got.Update.Auto {
		t.Error("auto should be untouched by turning the check off")
	}
}

func TestUpdateIntervalHours_FallsBackOnNonsense(t *testing.T) {
	for _, in := range []int{0, -1, -24} {
		c := Config{Update: UpdateConfig{IntervalHours: in}}
		if got := c.UpdateIntervalHours(); got != DefaultUpdateIntervalHours {
			t.Errorf("IntervalHours=%d yielded %d, want the default (a 0 would hot-loop the daemon)", in, got)
		}
	}
	c := Config{Update: UpdateConfig{IntervalHours: 6}}
	if got := c.UpdateIntervalHours(); got != 6 {
		t.Errorf("IntervalHours=6 yielded %d", got)
	}
}

func TestUpdateKeys_GetSetRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	for _, key := range UpdateKeys() {
		before, ok := cfg.GetBool(key)
		if !ok {
			t.Fatalf("GetBool(%q) not recognised", key)
		}
		if !cfg.SetBool(key, !before) {
			t.Fatalf("SetBool(%q) not recognised", key)
		}
		after, _ := cfg.GetBool(key)
		if after == before {
			t.Errorf("%s did not change", key)
		}
	}
}

// The "update." prefix is required: a bare "check"/"auto" must NOT silently
// resolve to something else, and an update key must not be confused for a note
// toggle.
func TestUpdateKeys_PrefixIsRequired(t *testing.T) {
	cfg := DefaultConfig()
	for _, key := range []string{"check", "auto", "note.auto", "update.nonsense"} {
		if _, ok := cfg.GetBool(key); ok {
			t.Errorf("GetBool(%q) resolved, want unknown", key)
		}
		if cfg.SetBool(key, true) {
			t.Errorf("SetBool(%q) resolved, want unknown", key)
		}
	}
}

// A config file written by a blamely that predates self-update has no "update"
// object at all. Because LoadConfig unmarshals ON TOP of the defaults, those
// machines adopt the shipped policy (check and auto both on) instead of reading
// as all-false — which is exactly how an existing fleet starts updating itself.
func TestUpdate_MissingSectionAdoptsDefaults(t *testing.T) {
	fakeHome(t)
	path, err := ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBlamelyDir(); err != nil {
		t.Fatal(err)
	}
	// A pre-update config: note settings only, no "update" key.
	if err := os.WriteFile(path, []byte(`{"note":{"tokens":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig()
	if !got.Update.Check || !got.Update.Auto {
		t.Errorf("update = %+v, want check on / auto on", got.Update)
	}
	if got.Note.Tokens {
		t.Error("the file's own settings must still win over the defaults")
	}
}
