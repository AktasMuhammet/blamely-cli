package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// releaseAPIBase is the GitHub releases API root for blamely. A var (not const)
// so tests can point it at a local server. The repo's releases are public (the
// install scripts download their assets unauthenticated), so no token is needed.
var releaseAPIBase = "https://api.github.com/repos/blamely-ai/blamely"

const releaseNotesTimeout = 5 * time.Second

type releaseInfo struct {
	Name    string `json:"name"`
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
}

// ReleaseChannel returns which release the notes should come from, mirroring the
// installer's BLAMELY_CHANNEL (default "latest", or "beta" for the pre-release).
func ReleaseChannel() string {
	if c := strings.TrimSpace(os.Getenv("BLAMELY_CHANNEL")); c != "" {
		return c
	}
	return "latest"
}

// FetchReleaseNotes returns the GitHub release name/tag/body for a channel tag.
func FetchReleaseNotes(ctx context.Context, channel string) (releaseInfo, error) {
	return fetchReleaseNotes(ctx, releaseAPIBase, channel)
}

func fetchReleaseNotes(ctx context.Context, base, channel string) (releaseInfo, error) {
	var info releaseInfo
	url := fmt.Sprintf("%s/releases/tags/%s", base, channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("github releases %s: status %d", channel, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, fmt.Errorf("decode release: %w", err)
	}
	return info, nil
}

// formatWhatsNew renders the post-install notes block, or "" when there's nothing
// worth showing (empty body) — so the caller can stay silent.
func formatWhatsNew(info releaseInfo) string {
	body := strings.TrimSpace(info.Body)
	if body == "" {
		return ""
	}
	title := info.Name
	if title == "" {
		title = info.TagName
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nWhat's new")
	if title != "" {
		fmt.Fprintf(&b, " (%s)", title)
	}
	b.WriteString(":\n")
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(&b, "  %s\n", strings.TrimRight(line, "\r"))
	}
	if info.TagName != "" {
		fmt.Fprintf(&b, "\nFull release: https://github.com/blamely-ai/blamely/releases/tag/%s\n", info.TagName)
	}
	return b.String()
}

// PrintWhatsNew fetches the current channel's GitHub release notes and prints them
// after an install. Best-effort: offline, rate-limited, opted-out, or an empty
// body all just print nothing — it must never fail or block the install. Set
// BLAMELY_NO_RELEASE_NOTES=1 to skip the network call entirely.
func PrintWhatsNew() {
	if v := strings.TrimSpace(os.Getenv("BLAMELY_NO_RELEASE_NOTES")); v != "" && v != "0" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), releaseNotesTimeout)
	defer cancel()
	info, err := FetchReleaseNotes(ctx, ReleaseChannel())
	if err != nil {
		return
	}
	if s := formatWhatsNew(info); s != "" {
		fmt.Print(s)
	}
}
