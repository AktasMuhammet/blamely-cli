package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchReleaseNotes_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/tags/latest" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"v1.6.3","tag_name":"latest","body":"- watermark resume\n- copilot tokens"}`))
	}))
	defer srv.Close()

	info, err := fetchReleaseNotes(context.Background(), srv.URL, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "v1.6.3" || info.TagName != "latest" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !strings.Contains(info.Body, "watermark resume") {
		t.Fatalf("body not parsed: %q", info.Body)
	}
}

func TestFetchReleaseNotes_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := fetchReleaseNotes(context.Background(), srv.URL, "1.6.3"); err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestFormatWhatsNew(t *testing.T) {
	out := formatWhatsNew(releaseInfo{Name: "v1.6.3", TagName: "latest", Body: "- a\r\n- b\n"})
	for _, want := range []string{"What's new (v1.6.3):", "  - a", "  - b", "releases/tag/latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "\r") {
		t.Error("CR not stripped from body lines")
	}

	// Empty body → nothing to show.
	if got := formatWhatsNew(releaseInfo{Name: "v1.6.3", TagName: "latest", Body: "  \n "}); got != "" {
		t.Errorf("empty body should render nothing, got %q", got)
	}
	// Falls back to tag name when name is empty.
	if got := formatWhatsNew(releaseInfo{TagName: "beta", Body: "x"}); !strings.Contains(got, "(beta)") {
		t.Errorf("expected tag fallback in title, got %q", got)
	}
}

func TestReleaseChannel(t *testing.T) {
	t.Setenv("BLAMELY_CHANNEL", "")
	if got := ReleaseChannel(); got != "latest" {
		t.Errorf("default channel = %q, want latest", got)
	}
	t.Setenv("BLAMELY_CHANNEL", "beta")
	if got := ReleaseChannel(); got != "beta" {
		t.Errorf("env channel = %q, want beta", got)
	}
}
