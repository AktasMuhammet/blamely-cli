package report

import (
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/gitnotes"
)

func TestReportEmbedsFontsNoGoogle(t *testing.T) {
	html, err := RenderHTML(&gitnotes.Note{}, commitMeta_{})
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(html, "googleapis") || strings.Contains(html, "gstatic") {
		t.Error("report still references Google Fonts (external call)")
	}
	for _, want := range []string{
		"@font-face",
		"IBM Plex Sans",
		"JetBrains Mono",
		"data:font/woff2;base64,",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered report missing %q", want)
		}
	}
	t.Logf("report size: %d KB, @font-face blocks: %d",
		len(html)/1024, strings.Count(html, "@font-face"))
}
