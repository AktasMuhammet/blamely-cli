//go:build windows

package install

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// editorImages are the Windows process images that keep Blamely's editor plugin
// (and the bundled sqlite3.exe the plugin loads from ~/.blamely/bin) locked
// while the editor is open. A locked file makes both the plugin and the bin
// directory undeletable, so the uninstall silently leaves them behind. Listed by
// friendly name so the prompt can tell the user exactly what to close.
var editorImages = []struct{ name, image string }{
	{"Visual Studio Code", "Code.exe"},
	{"VS Code Insiders", "Code - Insiders.exe"},
	{"Cursor", "Cursor.exe"},
	{"Windsurf", "Windsurf.exe"},
	{"IntelliJ IDEA", "idea64.exe"},
	{"PyCharm", "pycharm64.exe"},
	{"WebStorm", "webstorm64.exe"},
	{"GoLand", "goland64.exe"},
	{"PhpStorm", "phpstorm64.exe"},
	{"Rider", "rider64.exe"},
	{"CLion", "clion64.exe"},
	{"RubyMine", "rubymine64.exe"},
	{"DataGrip", "datagrip64.exe"},
	{"Android Studio", "studio64.exe"},
}

type blocker struct {
	name  string
	image string
	pids  []int
}

// promptCloseBlockers warns about editors that are open and would block the
// uninstall by locking Blamely's plugin / sqlite3 files, then offers to close
// them. Default is to leave them be (anything still locked falls to the deferred
// 3-pass cleanup); typing Y closes them so the removal succeeds this run.
func promptCloseBlockers() {
	blockers := runningBlockers()
	if len(blockers) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("  ⚠ These programs are open and hold Blamely files locked, which blocks uninstall:")
	for _, b := range blockers {
		fmt.Printf("      • %s (%s)\n", b.name, b.image)
	}
	fmt.Print("  Close them yourself and press Enter, or type Y to let Blamely close them now [y/N]: ")
	if !readAffirmative() {
		fmt.Println("  Continuing — anything still locked is cleared on the next uninstall or reboot.")
		return
	}
	for _, b := range blockers {
		for _, pid := range b.pids {
			_ = killProcess(pid) // bin_windows.go: taskkill /F /T /PID
		}
	}
	fmt.Println("  Closed. Continuing uninstall...")
}

func runningBlockers() []blocker {
	var out []blocker
	for _, e := range editorImages {
		if pids := pidsByImage(e.image); len(pids) > 0 {
			out = append(out, blocker{name: e.name, image: e.image, pids: pids})
		}
	}
	return out
}

// pidsByImage returns the PIDs of every running process with the given image
// name, parsed from `tasklist` CSV output (no header row).
func pidsByImage(image string) []int {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+image, "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	// tasklist prints a plain "INFO: No tasks..." line (not CSV) on no match.
	if !strings.HasPrefix(strings.TrimSpace(string(out)), `"`) {
		return nil
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	r.FieldsPerRecord = -1
	records, _ := r.ReadAll()
	var pids []int
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(rec[1])); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func readAffirmative() bool {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
