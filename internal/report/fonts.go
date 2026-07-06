package report

import (
	"embed"
	"encoding/base64"
	"fmt"
	"strings"
)

// Latin-subset woff2 for the report's two web fonts, bundled into the binary so
// the generated HTML embeds them as base64 data: URIs — the report renders in
// IBM Plex Sans / JetBrains Mono with NO request to fonts.googleapis.com (or any
// other host). This keeps a shared/emailed report faithful to the app's look
// while honouring Blamely's "nothing leaves your machine" guarantee.
//
//go:embed fonts/*.woff2
var fontFS embed.FS

// embeddedFaces lists every bundled face with the CSS family + weight it serves.
// Weights match what html_template.go uses (sans 400/500/600/700, mono 400/500/600).
var embeddedFaces = []struct{ file, family, weight string }{
	{"fonts/ibmplexsans-400.woff2", "IBM Plex Sans", "400"},
	{"fonts/ibmplexsans-500.woff2", "IBM Plex Sans", "500"},
	{"fonts/ibmplexsans-600.woff2", "IBM Plex Sans", "600"},
	{"fonts/ibmplexsans-700.woff2", "IBM Plex Sans", "700"},
	{"fonts/jetbrainsmono-400.woff2", "JetBrains Mono", "400"},
	{"fonts/jetbrainsmono-500.woff2", "JetBrains Mono", "500"},
	{"fonts/jetbrainsmono-600.woff2", "JetBrains Mono", "600"},
}

// fontFaceStyle builds a <style> block of @font-face rules with each woff2 inlined
// as a base64 data: URI. Built once and baked into the parsed template (see
// html_template.go). A face that fails to read is skipped — the CSS var stacks in
// the template already fall back to system fonts, so the report still renders.
func fontFaceStyle() string {
	var b strings.Builder
	b.WriteString("<style>")
	for _, f := range embeddedFaces {
		data, err := fontFS.ReadFile(f.file)
		if err != nil {
			continue
		}
		enc := base64.StdEncoding.EncodeToString(data)
		fmt.Fprintf(&b,
			"@font-face{font-family:'%s';font-style:normal;font-weight:%s;font-display:swap;"+
				"src:url(data:font/woff2;base64,%s) format('woff2')}",
			f.family, f.weight, enc)
	}
	b.WriteString("</style>")
	return b.String()
}
