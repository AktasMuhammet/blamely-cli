package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/daemon"
)

// jsEscapeForTest embeds a filesystem path in a JS string literal the way Codex
// does (a Windows path shows up as `D:\\Dev`).
func jsEscapeForTest(p string) string {
	p = strings.ReplaceAll(p, `\`, `\\`)
	return strings.ReplaceAll(p, `"`, `\"`)
}

// TestCodexExecScriptShellCalls covers the shape Codex 0.145 introduced: every
// tool goes through one `exec` call whose `input` is a JavaScript body, so the
// command is a JS string literal instead of a JSON field. Windows paths arrive
// backslash-escaped (`D:\\Dev`) and must be unescaped back.
func TestCodexExecScriptShellCalls(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantCmd     string
		wantWorkdir string
	}{
		{
			name:        "mixed quoted and bare keys",
			src:         `const r = await tools.shell_command({command:"Get-Content -LiteralPath 'src\\main\\App.java'","workdir":"D:\\Dev\\sol-ems-ims"}); text(r)`,
			wantCmd:     `Get-Content -LiteralPath 'src\main\App.java'`,
			wantWorkdir: `D:\Dev\sol-ems-ims`,
		},
		{
			name:        "with timeout arg after workdir",
			src:         `const r = await tools.shell_command({command:"mvn -Dtest=X test","workdir":"D:\\Dev","timeout_ms":120000}); text(r)`,
			wantCmd:     "mvn -Dtest=X test",
			wantWorkdir: `D:\Dev`,
		},
		{
			name:        "escaped quote inside the command",
			src:         `await tools.shell_command({command:"rg -n \"foo|bar\" src","workdir":"/tmp/x"})`,
			wantCmd:     `rg -n "foo|bar" src`,
			wantWorkdir: "/tmp/x",
		},
		{
			name:        "legacy cmd/cwd key names",
			src:         `await tools.exec_command({cmd:'rm -f a.txt', cwd:'/tmp/y'})`,
			wantCmd:     "rm -f a.txt",
			wantWorkdir: "/tmp/y",
		},
		{
			name:        "no workdir supplied",
			src:         `await tools.shell_command({command:"rm -f a.txt"})`,
			wantCmd:     "rm -f a.txt",
			wantWorkdir: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := codexExecScriptShellCalls(tc.src)
			// "tools.shell" is a prefix of "tools.shell_command" — the scan must
			// not report the same call twice.
			if len(got) != 1 {
				t.Fatalf("got %d invocations, want 1: %+v", len(got), got)
			}
			if got[0].Cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", got[0].Cmd, tc.wantCmd)
			}
			if got[0].Workdir != tc.wantWorkdir {
				t.Errorf("workdir = %q, want %q", got[0].Workdir, tc.wantWorkdir)
			}
		})
	}
}

// TestCodexExecScriptMultipleShellCalls verifies both commands are recovered
// when one exec script runs more than one shell command.
func TestCodexExecScriptMultipleShellCalls(t *testing.T) {
	src := `await tools.shell_command({command:"rm -f a.txt","workdir":"/tmp/one"});` +
		`await tools.shell_command({command:"rm -f b.txt","workdir":"/tmp/two"});`
	got := codexExecScriptShellCalls(src)
	if len(got) != 2 {
		t.Fatalf("got %d invocations, want 2: %+v", len(got), got)
	}
	if got[0].Cmd != "rm -f a.txt" || got[0].Workdir != "/tmp/one" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Cmd != "rm -f b.txt" || got[1].Workdir != "/tmp/two" {
		t.Errorf("second = %+v", got[1])
	}
}

// TestCodexExecCustomToolCallDeletion is the end-to-end regression for 0.145:
// a file Codex deletes through the new exec/custom_tool_call wrapper must still
// be attributed, instead of falling back to Human at commit time.
func TestCodexExecCustomToolCallDeletion(t *testing.T) {
	repo := newCodexTestRepo(t, "simple-login.html", "<html>\n<body>login</body>\n</html>\n")

	sink := &mockSink{}
	st := &codexState{sink: sink, model: "gpt-5.6-luna"}

	payload, _ := json.Marshal(map[string]any{
		"type":    "custom_tool_call",
		"id":      "ctc_052523be99593244",
		"status":  "completed",
		"call_id": "call_5294VAmvTHNG0z3t",
		"name":    "exec",
		"input": `const r = await tools.shell_command({command:"Remove-Item -LiteralPath ./simple-login.html",` +
			`"workdir":"` + jsEscapeForTest(repo) + `"}); text(r)` + "\n",
	})
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-08-06T12:49:01.475Z",
		"type":      "response_item",
		"payload":   json.RawMessage(payload),
	})

	processCodexLine(line, st)
	if len(st.pending) != 1 {
		t.Fatalf("expected 1 buffered deletion event, got %d", len(st.pending))
	}
	st.flush(0, 0, 0, 0, false)

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 recorded event, got %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Tool != "codex" {
		t.Errorf("tool = %q, want codex", ev.Tool)
	}
	if ev.FilePath != "simple-login.html" {
		t.Errorf("file = %q, want simple-login.html", ev.FilePath)
	}
	if len(ev.RemovedLines) == 0 {
		t.Errorf("expected removed-line hashes for the deleted file, got none")
	}
	if ev.RepoPath == "" {
		t.Errorf("RepoPath empty -> event would be dropped by the sink")
	}
}

// TestCodexExecDeletionInheritsSessionCwd covers an exec script that omits
// workdir: the cwd from session_meta stands in, so the deletion still resolves.
func TestCodexExecDeletionInheritsSessionCwd(t *testing.T) {
	repo := newCodexTestRepo(t, "gone.txt", "one\ntwo\n")

	st := &codexState{sink: &mockSink{}}
	meta, _ := json.Marshal(map[string]any{
		"timestamp": "2026-08-06T12:48:58.172Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":          "019fd71d",
			"cwd":         repo,
			"originator":  "codex-tui",
			"cli_version": "0.145.0",
			"source":      "cli",
		},
	})
	processCodexLine(meta, st)
	if st.cwd != repo {
		t.Fatalf("session cwd = %q, want %q", st.cwd, repo)
	}

	payload, _ := json.Marshal(map[string]any{
		"type":  "custom_tool_call",
		"name":  "exec",
		"input": `await tools.shell_command({command:"rm -f gone.txt"});`,
	})
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-08-06T12:49:01.475Z",
		"type":      "response_item",
		"payload":   json.RawMessage(payload),
	})
	processCodexLine(line, st)
	if len(st.pending) != 1 {
		t.Fatalf("expected the deletion to resolve against the session cwd, got %d events", len(st.pending))
	}
}

// TestCodexTokenCountCacheWrite verifies the cache-write counter 0.145 added is
// recorded, and that a log without it still reports zero rather than failing.
func TestCodexTokenCountCacheWrite(t *testing.T) {
	for _, tc := range []struct {
		name      string
		payload   string
		wantWrite int64
	}{
		{
			name:      "0.145 reports cache_write_input_tokens",
			payload:   `{"type":"token_count","info":{"last_token_usage":{"input_tokens":8909,"cached_input_tokens":12,"cache_write_input_tokens":8906,"output_tokens":153}}}`,
			wantWrite: 8906,
		},
		{
			name:      "older log omits it",
			payload:   `{"type":"token_count","info":{"last_token_usage":{"input_tokens":8909,"cached_input_tokens":12,"output_tokens":153}}}`,
			wantWrite: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &mockSink{}
			st := &codexState{
				sink:    sink,
				pending: []daemon.Event{{Tool: "codex", RepoPath: "/repo", FilePath: "a.go"}},
			}
			flushCodexTokenCount(json.RawMessage(tc.payload), st)
			if len(sink.events) != 1 {
				t.Fatalf("expected 1 flushed event, got %d", len(sink.events))
			}
			ev := sink.events[0]
			if ev.InputTokens == nil || *ev.InputTokens != 8909 {
				t.Errorf("input tokens = %v, want 8909", ev.InputTokens)
			}
			if ev.CacheReadTokens == nil || *ev.CacheReadTokens != 12 {
				t.Errorf("cache read = %v, want 12", ev.CacheReadTokens)
			}
			if ev.CacheWriteTokens == nil || *ev.CacheWriteTokens != tc.wantWrite {
				t.Errorf("cache write = %v, want %d", ev.CacheWriteTokens, tc.wantWrite)
			}
		})
	}
}

// TestCodexWorldStateEnvelopeIgnored guards the new 0.145 `world_state` line:
// it must be recognized as a wrapped envelope and ignored, never handed to the
// flat parser.
func TestCodexWorldStateEnvelopeIgnored(t *testing.T) {
	line := []byte(`{"timestamp":"2026-08-06T12:48:58.186Z","type":"world_state","payload":{"full":true,"state":{"collaboration_mode":"default","permissions":"32ca25d6"}}}`)
	env, ok := codexWrappedEnvelope(line)
	if !ok {
		t.Fatalf("world_state not recognized as a wrapped envelope")
	}
	if env.Type != "world_state" {
		t.Fatalf("type = %q", env.Type)
	}
	st := &codexState{sink: &mockSink{}}
	processCodexLine(line, st)
	if len(st.pending) != 0 || st.model != "" {
		t.Errorf("world_state should be inert, got pending=%d model=%q", len(st.pending), st.model)
	}
}

// TestReadCodexSessionUsageWrapped covers the hook path against a wrapped-format
// transcript (0.13x through 0.145) — model from turn_context, usage from
// event_msg/token_count.
func TestReadCodexSessionUsageWrapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-2026-08-06T15-47-16.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-06T12:48:58.172Z","type":"session_meta","payload":{"id":"019fd71d","cwd":"D:\\Dev","originator":"codex-tui","cli_version":"0.145.0","source":"cli"}}`,
		`{"timestamp":"2026-08-06T12:48:58.186Z","type":"turn_context","payload":{"cwd":"D:\\Dev","model":"gpt-5.6-luna","effort":"medium"}}`,
		`{"timestamp":"2026-08-06T12:49:11.347Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10501,"cached_input_tokens":8906,"cache_write_input_tokens":1592,"output_tokens":104}}}}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := ReadCodexSessionUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		t.Fatal("no usage found in a wrapped-format transcript")
	}
	if u.Model != "gpt-5.6-luna" {
		t.Errorf("model = %q, want gpt-5.6-luna", u.Model)
	}
	if u.InputTokens != 10501 || u.OutputTokens != 104 {
		t.Errorf("tokens in/out = %d/%d, want 10501/104", u.InputTokens, u.OutputTokens)
	}
	if u.CacheReadTokens != 8906 || u.CacheWriteTokens != 1592 {
		t.Errorf("cache read/write = %d/%d, want 8906/1592", u.CacheReadTokens, u.CacheWriteTokens)
	}
}

// TestCodexAllFormatsSupported runs one full mini-transcript per known session
// format through the same parser and asserts each produces the same attribution:
// an edit (added lines) plus a shell deletion (removed lines), both carrying the
// turn's model and tokens. One binary must handle every format, since a user's
// ~/.codex/sessions holds rollouts written by whichever Codex version was
// installed at the time.
func TestCodexAllFormatsSupported(t *testing.T) {
	tests := []struct {
		name  string
		lines func(repo, edited, doomed string) []string
	}{
		{
			// Pre-0.13x: flat lines, no envelope. apply_patch carries the patch
			// body; usage arrives as a top-level block. No shell-deletion signal
			// existed in this format, so only the edit is expected (asserted below).
			name: "legacy flat",
			lines: func(repo, edited, doomed string) []string {
				patch := "*** Begin Patch\n*** Update File: " + edited + "\n@@\n+added by codex\n*** End Patch\n"
				args, _ := json.Marshal(map[string]string{"input": patch})
				call, _ := json.Marshal(map[string]any{
					"timestamp": "2026-08-06T12:49:01.000Z",
					"type":      "function_call",
					"name":      "apply_patch",
					"model":     "gpt-5.6-luna",
					"arguments": string(args),
				})
				return []string{
					string(call),
					`{"type":"response.complete","usage":{"input_tokens":10501,"output_tokens":104,"cache_read_input_tokens":8906}}`,
				}
			},
		},
		{
			// 0.13x–0.144: wrapped envelope; shell exec is a function_call whose
			// arguments are a JSON string-of-JSON.
			name: "wrapped 0.144",
			lines: func(repo, edited, doomed string) []string {
				inner, _ := json.Marshal(map[string]string{
					"command": "rm -f " + filepath.Base(doomed),
					"workdir": repo,
				})
				del, _ := json.Marshal(map[string]any{
					"timestamp": "2026-08-06T12:49:01.000Z",
					"type":      "response_item",
					"payload": map[string]any{
						"type":      "function_call",
						"name":      "shell_command",
						"arguments": string(inner),
					},
				})
				return []string{
					`{"timestamp":"2026-08-06T12:48:58.000Z","type":"session_meta","payload":{"originator":"codex-tui","source":"cli","cwd":"` + jsonEscapeForTest(repo) + `","cli_version":"0.142.0"}}`,
					`{"timestamp":"2026-08-06T12:48:58.100Z","type":"turn_context","payload":{"model":"gpt-5.6-luna"}}`,
					string(del),
					codexPatchApplyLineForTest(edited),
					`{"timestamp":"2026-08-06T12:49:11.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10501,"cached_input_tokens":8906,"output_tokens":104}}}}`,
				}
			},
		},
		{
			// 0.145+: wrapped envelope plus world_state; shell exec is a
			// custom_tool_call named "exec" whose input is a JavaScript body.
			name: "wrapped 0.145",
			lines: func(repo, edited, doomed string) []string {
				del, _ := json.Marshal(map[string]any{
					"timestamp": "2026-08-06T12:49:01.000Z",
					"type":      "response_item",
					"payload": map[string]any{
						"type":    "custom_tool_call",
						"name":    "exec",
						"call_id": "call_5294VAmvTHNG0z3t",
						"input": `const r = await tools.shell_command({command:"rm -f ` + filepath.Base(doomed) +
							`","workdir":"` + jsEscapeForTest(repo) + `"}); text(r)` + "\n",
					},
				})
				return []string{
					`{"timestamp":"2026-08-06T12:48:58.000Z","type":"session_meta","payload":{"originator":"codex-tui","source":"cli","cwd":"` + jsonEscapeForTest(repo) + `","cli_version":"0.145.0","thread_source":"user","history_mode":"legacy"}}`,
					`{"timestamp":"2026-08-06T12:48:58.050Z","type":"world_state","payload":{"full":true,"state":{"collaboration_mode":"default"}}}`,
					`{"timestamp":"2026-08-06T12:48:58.100Z","type":"turn_context","payload":{"model":"gpt-5.6-luna","cwd":"` + jsonEscapeForTest(repo) + `"}}`,
					string(del),
					codexPatchApplyLineForTest(edited),
					`{"timestamp":"2026-08-06T12:49:11.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10501,"cached_input_tokens":8906,"cache_write_input_tokens":1592,"output_tokens":104}}}}`,
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newCodexTestRepo(t, "doomed.txt", "one\ntwo\n")
			// Second file: edited in place rather than deleted.
			edited := filepath.Join(repo, "edited.txt")
			if err := os.WriteFile(edited, []byte("keep\nadded by codex\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			doomed := filepath.Join(repo, "doomed.txt")

			sink := &mockSink{}
			st := &codexState{sink: sink}
			for _, l := range tc.lines(repo, edited, doomed) {
				processCodexLine([]byte(l), st)
			}
			st.flush(0, 0, 0, 0, false) // drain anything a format left unflushed

			var edits, deletions int
			for _, ev := range sink.events {
				if ev.Tool != "codex" {
					t.Errorf("tool = %q, want codex", ev.Tool)
				}
				if ev.RepoPath == "" {
					t.Errorf("%s: RepoPath empty -> the sink would drop this event", ev.FilePath)
				}
				if len(ev.Lines) > 0 {
					edits++
				}
				if len(ev.RemovedLines) > 0 && len(ev.Lines) == 0 {
					deletions++
				}
			}
			if edits == 0 {
				t.Errorf("no edit attributed; events: %+v", sink.events)
			}
			// Only the wrapped formats carry a shell-exec signal to detect.
			if tc.name != "legacy flat" && deletions == 0 {
				t.Errorf("shell deletion not attributed; events: %+v", sink.events)
			}
			// Model and tokens must reach every event regardless of format.
			for _, ev := range sink.events {
				if ev.Model != "gpt-5.6-luna" {
					t.Errorf("%s: model = %q, want gpt-5.6-luna", ev.FilePath, ev.Model)
				}
				if ev.InputTokens == nil || *ev.InputTokens != 10501 {
					t.Errorf("%s: input tokens = %v, want 10501", ev.FilePath, ev.InputTokens)
				}
				if ev.OutputTokens == nil || *ev.OutputTokens != 104 {
					t.Errorf("%s: output tokens = %v, want 104", ev.FilePath, ev.OutputTokens)
				}
			}
			t.Logf("%s: %d event(s), %d edit(s), %d deletion(s)", tc.name, len(sink.events), edits, deletions)
		})
	}
}

// codexPatchApplyLineForTest builds a wrapped patch_apply_end line that adds one
// line to path.
func codexPatchApplyLineForTest(path string) string {
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-08-06T12:49:05.000Z",
		"type":      "event_msg",
		"payload": map[string]any{
			"type":    "patch_apply_end",
			"success": true,
			"changes": map[string]any{
				path: map[string]any{
					"type":         "update",
					"unified_diff": "@@ -1,1 +1,2 @@\n keep\n+added by codex\n",
				},
			},
		},
	})
	return string(line)
}

// jsonEscapeForTest embeds a path inside a hand-written JSON string literal.
func jsonEscapeForTest(p string) string {
	b, _ := json.Marshal(p)
	return string(b[1 : len(b)-1])
}

// newCodexTestRepo builds a git repo containing name, commits it, then removes
// the file from disk — the state a shell deletion leaves behind.
func newCodexTestRepo(t *testing.T, name, content string) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "add "+name)
	if err := os.Remove(filepath.Join(repo, name)); err != nil {
		t.Fatal(err)
	}
	return repo
}
