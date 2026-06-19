package tools

// CursorChatWatcher reads Cursor's chat-session JSONL files and attributes them
// to the cursor tool. It only watches Cursor's own workspaceStorage directory.
//
// Copilot sessions in VS Code are handled by CopilotChatWatcher (copilot_chat.go).
// The two watchers are fully independent: they watch different directories, carry
// different fixed tool values, and share no mutable state.

import (
	"os"
	"path/filepath"
	"runtime"

	"context"
	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/store"
)

// CursorChatWatcher implements daemon.Watcher for Cursor's built-in chat panel.
// It watches Cursor/User/workspaceStorage and always emits tool=cursor.
type CursorChatWatcher struct {
	// Roots overrides the workspaceStorage scan roots for tests.
	Roots []string
	// DB, when set, persists per-file read offsets across daemon restarts.
	DB *store.DB
}

func (c *CursorChatWatcher) Name() string { return "cursor-chat" }

func (c *CursorChatWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	roots := c.Roots
	if len(roots) == 0 {
		roots = defaultCursorChatRoots()
	}
	return (&chatSessionWatcher{tool: store.ToolCursor, roots: roots, name: c.Name(), db: c.DB}).run(ctx, sink)
}

// defaultCursorChatRoots returns the Cursor workspaceStorage paths for the
// current OS. Only Cursor — VS Code is handled by CopilotChatWatcher.
func defaultCursorChatRoots() []string {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return nil
		}
		return []string{
			filepath.Join(appData, "Cursor", "User", "workspaceStorage"),
		}
	default:
		return []string{
			filepath.Join(home, ".config", "Cursor", "User", "workspaceStorage"),
		}
	}
}
