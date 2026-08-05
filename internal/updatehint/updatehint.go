// Package updatehint stores the daemon's answer to "is a newer blamely
// available?" so the CLI can surface it without making a network call of its own.
//
// It is a leaf package on purpose. The daemon writes the hint and
// `blamely status` / `blamely doctor` read it, but internal/daemon must not
// import internal/install (install already imports daemon — that would be a
// cycle), so the file format lives here where both sides can reach it.
package updatehint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/blamely/blamely/internal/config"
)

const fileName = "update-available.json"

// Hint is the recorded result of the last successful check. Its presence means
// "a newer version existed as of CheckedAt"; absence means up to date, never
// checked, or the check could not reach the network.
type Hint struct {
	Version   string    `json:"version"`
	Tag       string    `json:"tag"`
	URL       string    `json:"url,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Path returns ~/.blamely/update-available.json.
func Path() (string, error) {
	d, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, fileName), nil
}

// Read returns the stored hint. ok=false when there is none, or when the file is
// unreadable/corrupt — a bad hint is simply "no hint", never an error the caller
// has to handle.
func Read() (h Hint, ok bool) {
	path, err := Path()
	if err != nil {
		return Hint{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Hint{}, false
	}
	if json.Unmarshal(data, &h) != nil || h.Version == "" {
		return Hint{}, false
	}
	return h, true
}

// Write stores the hint atomically: a temp file IN THE SAME DIRECTORY, then
// rename. Same-directory staging matters — os.Rename across filesystems fails
// with EXDEV — and the rename keeps a concurrent reader from ever seeing a
// half-written file.
func Write(h Hint) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if _, err := config.EnsureBlamelyDir(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+fileName+".*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Clear removes the hint (we are up to date). Missing file is success.
func Clear() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
