//go:build windows

package install

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"

	"github.com/blamely/blamely/internal/procattr"
)

// CheckAutostartTasks reports Scheduled Tasks that are registered but do NOT run
// what this build would register.
//
// Why this exists: `schtasks /Create /F` fails with "Access is denied" against a
// task registered from an ELEVATED session, even for the same user. Both the
// installer (isSchtasksAccessDenied → the unprivileged fallback) and the daemon's
// self-heal (EnsureDaemonAgent → "the logon task exists, good enough") swallow
// that failure, so a machine whose tasks were created by an admin install keeps
// running the OLD command line — and every autostart fix we ship, including the
// windowless launcher, silently never arrives. That state was invisible: install
// printed a green "Daemon agent" row and doctor said nothing.
//
// Comparing what is registered against what we would register is what makes it
// visible, and it catches the whole class, not just the elevation case (a task
// left over from a different install path or an older layout looks the same).
//
// A task we cannot READ is not reported: an unreadable registration proves
// nothing, and a false "your autostart is broken" is worse than silence.
func CheckAutostartTasks(binaryPath string) []AgentTaskIssue {
	expected := daemonCommand(binaryPath)
	want := normalizeCommand(expected)

	var issues []AgentTaskIssue
	for _, name := range []string{scheduledTaskName, keepaliveTaskName} {
		registered, ok := registeredTaskCommand(name)
		if !ok || normalizeCommand(registered) == want {
			continue
		}
		issues = append(issues, AgentTaskIssue{
			Task:       name,
			Registered: registered,
			Expected:   expected,
			Fix: fmt.Sprintf(
				`in an elevated PowerShell: schtasks /Delete /F /TN "%s"  — then run: blamely install`,
				name),
		})
	}
	return issues
}

// registeredTaskCommand returns the command line a Scheduled Task is registered
// to run, as `"<exe>" <args>`.
//
// Read from /XML rather than /V /FO LIST because the list format's field names
// are LOCALIZED ("Task To Run" is "Çalıştırılacak Görev" on a Turkish Windows),
// and parsing them would work on the developer's machine and fail on the
// customer's. The XML tag names are the same everywhere.
func registeredTaskCommand(taskName string) (string, bool) {
	out, err := procattr.Hide(exec.Command("schtasks", "/Query", "/TN", taskName, "/XML")).Output()
	if err != nil {
		return "", false // not registered, or not readable by us
	}
	doc := string(decodeMaybeUTF16(out))
	exe := xmlTagValue(doc, "Command")
	if exe == "" {
		return "", false
	}
	if args := xmlTagValue(doc, "Arguments"); args != "" {
		return `"` + exe + `" ` + args, true
	}
	return `"` + exe + `"`, true
}

// decodeMaybeUTF16 converts schtasks' XML to bytes Go can read. The document
// DECLARES encoding="UTF-16" and is sometimes actually written as UTF-16LE
// (with or without a BOM) and sometimes as plain bytes, depending on how stdout
// is attached — so the encoding is sniffed, not trusted.
func decodeMaybeUTF16(b []byte) []byte {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return utf16LE(b[2:])
	}
	// No BOM: a UTF-16LE document has a NUL as the second byte of every ASCII
	// character, which UTF-8 text never has this early.
	if len(b) >= 4 && b[1] == 0x00 && b[3] == 0x00 {
		return utf16LE(b)
	}
	return b
}

func utf16LE(b []byte) []byte {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(b[i:]))
	}
	return []byte(string(utf16.Decode(units)))
}

// xmlTagValue pulls the text of the first <tag>…</tag> out of doc.
//
// A string scan, not encoding/xml: the document declares an encoding that does
// not match its bytes, which makes the real parser refuse it outright
// ("encoding \"UTF-16\" declared but Decoder.CharsetReader is nil"), and the two
// tags wanted here are flat text in a fixed, Microsoft-generated schema.
func xmlTagValue(doc, tag string) string {
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(doc, open)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return unescapeXML(strings.TrimSpace(rest[:j]))
}

func unescapeXML(s string) string {
	for _, r := range []struct{ from, to string }{
		{"&quot;", `"`}, {"&apos;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&amp;", "&"},
	} {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	return s
}

// normalizeCommand reduces a command line to what actually matters for "is this
// the same command": quoting and letter case are not differences on Windows
// (schtasks re-quotes what it stores, and paths are case-insensitive), while
// spacing inside is collapsed so `daemon  --background` matches
// `daemon --background`.
func normalizeCommand(cmd string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(cmd, `"`, "")), " "))
}
