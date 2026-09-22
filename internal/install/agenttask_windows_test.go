//go:build windows

package install

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// toUTF16LE renders text the way schtasks /XML sometimes writes it.
func toUTF16LE(t *testing.T, s string, bom bool) []byte {
	t.Helper()
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(units)*2+2)
	if bom {
		b = append(b, 0xFF, 0xFE)
	}
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

// schtasks declares encoding="UTF-16" but delivers UTF-16LE (with or without a
// BOM) or plain bytes depending on how stdout is attached, so all three shapes
// have to read back the same.
func TestDecodeMaybeUTF16(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-16"?><Task><Command>C:\x\blamelyw.exe</Command></Task>`
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"utf16 with bom", toUTF16LE(t, doc, true)},
		{"utf16 without bom", toUTF16LE(t, doc, false)},
		{"plain bytes", []byte(doc)},
	} {
		if got := string(decodeMaybeUTF16(c.in)); got != doc {
			t.Errorf("%s: decoded %q", c.name, got)
		}
	}
}

func TestXMLTagValue(t *testing.T) {
	doc := `<Task>
  <Actions Context="Author">
    <Exec>
      <Command>C:\Users\a b\.blamely\bin\blamely.exe</Command>
      <Arguments>daemon --background</Arguments>
    </Exec>
  </Actions>
</Task>`
	if got := xmlTagValue(doc, "Command"); got != `C:\Users\a b\.blamely\bin\blamely.exe` {
		t.Errorf("Command = %q", got)
	}
	if got := xmlTagValue(doc, "Arguments"); got != "daemon --background" {
		t.Errorf("Arguments = %q", got)
	}
	if got := xmlTagValue(doc, "Missing"); got != "" {
		t.Errorf("missing tag = %q, want empty", got)
	}
	// Task XML escapes what it stores; an unescaped value would never match the
	// command line we build and would be reported as a false mismatch.
	if got := xmlTagValue(`<Arguments>--flag &quot;a b&quot; &amp; c</Arguments>`, "Arguments"); got != `--flag "a b" & c` {
		t.Errorf("escaped Arguments = %q", got)
	}
}

// Quoting and case are not real differences on Windows: schtasks re-quotes what
// it stores and paths are case-insensitive. Treating them as differences would
// make doctor cry wolf on every healthy machine.
func TestNormalizeCommand(t *testing.T) {
	same := []string{
		`"C:\Users\a\.blamely\bin\blamelyw.exe"`,
		`C:\Users\a\.blamely\bin\blamelyw.exe`,
		`"c:\users\a\.blamely\bin\blamelyw.exe"`,
	}
	want := normalizeCommand(same[0])
	for _, s := range same[1:] {
		if got := normalizeCommand(s); got != want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", s, got, want)
		}
	}
	if normalizeCommand(`"x.exe" daemon  --background`) != normalizeCommand(`x.exe daemon --background`) {
		t.Error("internal spacing should not be a difference")
	}
	if normalizeCommand(`"x.exe"`) == normalizeCommand(`"x.exe" daemon --background`) {
		t.Error("a different argument list MUST be a difference")
	}
}
