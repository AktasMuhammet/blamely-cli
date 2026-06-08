//go:build windows

package install

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/blamely/blamely/internal/config"
	"golang.org/x/sys/windows/registry"
)

// InstallPathEntry appends ~/.blamely/bin to the per-user Path registry value
// (HKCU\Environment). Returns the bin directory path, whether it was newly
// added, and any error.
func InstallPathEntry() (string, bool, error) {
	entry, err := blamelyBinDir()
	if err != nil {
		return "", false, err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return entry, false, fmt.Errorf("open user Environment key: %w", err)
	}
	defer k.Close()

	current, _, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return entry, false, err
	}
	if pathEntryPresent(current, entry) {
		return entry, false, nil
	}
	newPath := current
	if newPath != "" && !strings.HasSuffix(newPath, ";") {
		newPath += ";"
	}
	newPath += entry
	if err := k.SetStringValue("Path", newPath); err != nil {
		return entry, false, err
	}
	broadcastEnvironmentChange()
	return entry, true, nil
}

// UninstallPathEntry removes ~/.blamely/bin from the per-user Path registry.
func UninstallPathEntry(_ string) (string, bool, error) {
	entry, err := blamelyBinDir()
	if err != nil {
		return "", false, err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return entry, false, err
	}
	defer k.Close()

	current, _, err := k.GetStringValue("Path")
	if err != nil {
		if err == registry.ErrNotExist {
			return entry, false, nil
		}
		return entry, false, err
	}
	parts := strings.Split(current, ";")
	var kept []string
	removed := false
	for _, p := range parts {
		if p == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p), entry) {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	if !removed {
		return entry, false, nil
	}
	if err := k.SetStringValue("Path", strings.Join(kept, ";")); err != nil {
		return entry, false, err
	}
	broadcastEnvironmentChange()
	return entry, true, nil
}

func blamelyBinDir() (string, error) {
	d, err := config.BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bin"), nil
}

func pathEntryPresent(pathValue, entry string) bool {
	for _, p := range strings.Split(pathValue, ";") {
		if strings.EqualFold(strings.TrimSpace(p), entry) {
			return true
		}
	}
	return false
}

// broadcastEnvironmentChange nudges running shells to reload environment
// variables sooner than the next full logoff.
func broadcastEnvironmentChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	envName, _ := syscall.UTF16PtrFromString("Environment")
	_, _, _ = sendMessageTimeout.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(envName)),
		smtoAbortIfHung,
		5000,
		0,
	)
}
