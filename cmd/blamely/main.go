package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitnotes"
	"github.com/blamely/blamely/internal/install"
	"github.com/blamely/blamely/internal/report"
	"github.com/blamely/blamely/internal/tools"
)

// version is overridable at link time via `-ldflags "-X main.version=<tag>"`.
// The release workflow injects the git tag here; local dev builds use the
// fallback value below.
var version = "0.1.0-dev"

func main() {
	root := &cobra.Command{
		Use:   "blamely",
		Short: "Trace code changes and attribute them to AI tools or humans",
		Long: "Blamely watches your filesystem and AI-tool logs, then writes a per-line\n" +
			"AI-vs-human attribution report as a git note on every commit.",
		Version: version,
	}
	// Suppress Cobra's auto-generated `completion` command from the menu;
	// it's framework boilerplate and not a blamely feature.
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(cmdDaemon())
	root.AddCommand(cmdInstall())
	root.AddCommand(cmdUninstall())
	root.AddCommand(cmdRepair())
	root.AddCommand(cmdDetect())
	root.AddCommand(cmdRecord())
	root.AddCommand(cmdAttribute())
	root.AddCommand(cmdReport())
	root.AddCommand(cmdBlame())
	root.AddCommand(cmdStats())
	root.AddCommand(cmdHistory())
	root.AddCommand(cmdStatus())
	root.AddCommand(cmdDoctor())
	root.AddCommand(cmdLog())
	root.AddCommand(cmdConfig())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdDaemon() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived attribution daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Watchers use direct, observable signals (hooks, log parsers,
			// editor plugin events). The velocity/heuristic watcher has been
			// removed: inline completions are now attributed at high confidence
			// by the VS Code and IntelliJ plugins via the
			// editor.action.inlineSuggest.commit command and AnActionListener
			// APIs respectively, both of which POST directly to /edit.
			daemon.Watchers = []daemon.Watcher{
				&tools.CodexWatcher{},
				// Cursor: file-history presence signal + log events + chat panel.
				// These three are independent of Copilot and must not share state.
				&tools.CursorWatcher{},
				&tools.CursorLogWatcher{},
				&tools.CursorChatWatcher{},
				// Copilot: storage-touch signal + chat panel (VS Code only) + logs.
				// CopilotChatWatcher watches Code/workspaceStorage; CursorChatWatcher
				// watches Cursor/workspaceStorage. They never overlap.
				&tools.CopilotWatcher{},
				&tools.CopilotChatWatcher{},
				&tools.CopilotLogWatcher{},
				// Antigravity IDE's bundled Gemini agent: no CLI hook fires
				// (it never goes through Gemini CLI's BeforeTool/AfterTool
				// framework), so this tails the agent's own transcript logs.
				&tools.AntigravityGeminiWatcher{},
			}
			return daemon.Run(cmd.Context())
		},
	}
}

func cmdRepair() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "repair",
		Short: "Find and remove stale blamely/blamely-cli hooks left in repos",
		Long: "Scans your home directory for .git/hooks/post-commit files written by\n" +
			"an old blamely or blamely-cli installation and removes them.\n" +
			"The global core.hooksPath hook installed by `blamely install` takes over.",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := install.Repair(dryRun)
			if err != nil {
				return err
			}
			if len(result.Found) == 0 {
				fmt.Println("No stale blamely hooks found.")
				return nil
			}
			if dryRun {
				fmt.Printf("Would remove %d stale hook(s):\n", len(result.Found))
				for _, f := range result.Found {
					fmt.Printf("  - %s\n", f)
				}
				fmt.Println("\nRun without --dry-run to remove them.")
				return nil
			}
			for _, p := range result.Removed {
				fmt.Printf("  ✓ removed %s\n", p)
			}
			for _, e := range result.Errors {
				fmt.Printf("  ✗ %s\n", e)
			}
			fmt.Printf("\nRemoved %d stale hook(s). Your commits will now use the global hook at\n", len(result.Removed))
			if hooksDir, err := install.GitHooksDirPath(); err == nil {
				fmt.Printf("  %s/post-commit\n", hooksDir)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without actually removing")
	return c
}

func cmdDetect() *cobra.Command {
	return &cobra.Command{
		Use:   "detect",
		Short: "Print which AI tools Blamely found on this machine (read-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := install.Detect()
			if err != nil {
				return err
			}
			for _, row := range []struct {
				name string
				p    install.ToolPresence
			}{
				{"Claude Code", d.Claude},
				{"Cursor", d.Cursor},
				{"Codex CLI", d.Codex},
				{"GitHub Copilot", d.Copilot},
				{"Gemini CLI", d.Gemini},
			} {
				mark := "absent"
				if row.p.Present {
					mark = "found"
				}
				fmt.Printf("%-16s %s\n", row.name, mark)
				for _, h := range row.p.Hints {
					fmt.Printf("  - %s\n", h)
				}
			}
			return nil
		},
	}
}

func cmdInstall() *cobra.Command {
	var skipPlugins bool
	c := &cobra.Command{
		Use:   "install",
		Short: "Install Blamely (Claude hook, global git hook, daemon agent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return install.Run(!skipPlugins)
		},
	}
	c.Flags().BoolVar(&skipPlugins, "skip-plugins", false,
		"skip installing the VS Code-family/JetBrains IDE plugins (CLI + hooks only — handy for local dev builds)")
	return c
}

func cmdUninstall() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Reverse `blamely install`",
		RunE: func(cmd *cobra.Command, args []string) error {
			return install.Uninstall()
		},
	}
}

func cmdRecord() *cobra.Command {
	c := &cobra.Command{
		Use: "record <tool>",
		// Hidden — this is the entry point each AI tool's PostToolUse hook
		// calls (see internal/install/{claude,cursor,...}hook.go). Not for
		// direct user invocation.
		Hidden: true,
		Short:  "Internal: ingest an AI-tool edit event from stdin",
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "claude", "cursor":
				// Claude Code and Cursor share the PostToolUse payload shape;
				// the handler distinguishes them via `cursor_version`.
				return tools.RecordClaudeFromStdin(os.Stdin)
			case "codex":
				return tools.RecordCodexFromStdin(os.Stdin)
			case "copilot":
				return tools.RecordCopilotFromStdin(os.Stdin)
			case "gemini":
				return tools.RecordGeminiFromStdin(os.Stdin)
			default:
				return fmt.Errorf("unknown tool %q (supported: claude, cursor, codex, copilot, gemini)", args[0])
			}
		},
	}
	return c
}

func cmdAttribute() *cobra.Command {
	var quiet bool
	c := &cobra.Command{
		Use: "attribute <repo> <sha>",
		// Hidden from the user-facing menu — this is called by the global
		// post-commit hook (see internal/install/hookspath.go) with the
		// repo path + commit sha. End users never invoke it manually.
		Hidden: true,
		Short:  "Internal: compute attribution for a commit and write the git note",
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Self-heal: drop any stale per-repo Blamely hooks/runner left by
			// older installs. Hooks live globally (core.hooksPath) now, so the
			// per-repo copies are redundant and the legacy pre-push runner
			// recursed. Best-effort; never blocks the commit.
			install.RemoveLegacyRepoHooks(args[0])

			note, err := gitnotes.AttributeAndWrite(args[0], args[1])
			if err != nil {
				return err
			}
			if !quiet && note != nil {
				report.RenderBar(os.Stdout, note, 40)
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress the AI/Human bar after writing the note")
	return c
}

func cmdReport() *cobra.Command {
	var since string
	c := &cobra.Command{
		Use:   "report [<sha>]",
		Short: "Show line-by-line attribution for a commit, or an aggregated table",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return report.RenderCommit(args[0])
			}
			return report.RenderSince(since)
		},
	}
	c.Flags().StringVar(&since, "since", "7d", "time window for the aggregated table (e.g. 1d, 7d)")
	return c
}

func cmdStats() *cobra.Command {
	return &cobra.Command{
		Use:   "stats [<sha>]",
		Short: "Deep single-commit view: +added/-deleted, per-tool, gen-type, tokens, session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sha := "HEAD"
			if len(args) == 1 {
				sha = args[0]
			}
			return report.RenderStats(sha)
		},
	}
}

func cmdBlame() *cobra.Command {
	var rev string
	c := &cobra.Command{
		Use:   "blame <file>",
		Short: "Per-line attribution for a file: who wrote each line — human or AI tool/model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return report.RenderBlame(args[0], rev)
		},
	}
	c.Flags().StringVar(&rev, "rev", "HEAD", "revision to blame (commit, branch, tag)")
	return c
}

func cmdHistory() *cobra.Command {
	var since string
	var all bool
	c := &cobra.Command{
		Use:   "history",
		Short: "Aggregate report across all noted commits: totals, tools, tokens, coding time",
		RunE: func(cmd *cobra.Command, args []string) error {
			var d report.HistoryOptions
			d.AllRepos = all
			if since != "" {
				dur, err := parseDuration(since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				d.Since = dur
			}
			return report.RenderHistory(d)
		},
	}
	c.Flags().StringVar(&since, "since", "30d", "time window, e.g. 7d, 30d, 90d, 1y")
	c.Flags().BoolVar(&all, "all", false, "include all tracked repos, not just the current one")
	return c
}

// parseDuration extends time.ParseDuration with 'd' (days) and 'y' (years).
func parseDuration(s string) (time.Duration, error) {
	if len(s) >= 2 {
		switch s[len(s)-1] {
		case 'd':
			var n int
			if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err == nil {
				return time.Duration(n) * 24 * time.Hour, nil
			}
		case 'y':
			var n int
			if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err == nil {
				return time.Duration(n) * 365 * 24 * time.Hour, nil
			}
		}
	}
	return time.ParseDuration(s)
}

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status, detected tools, and recent activity",
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.PrintStatus()
		},
	}
}

func cmdLog() *cobra.Command {
	parent := &cobra.Command{
		Use:   "log",
		Short: "Live-tail tool logs for debugging attribution",
	}
	parent.AddCommand(cmdLogCursor())
	parent.AddCommand(cmdLogCopilot())
	parent.AddCommand(cmdLogCodex())
	parent.AddCommand(cmdLogClaude())
	parent.AddCommand(cmdLogGemini())
	return parent
}

func cmdLogCopilot() *cobra.Command {
	return &cobra.Command{
		Use:   "copilot",
		Short: "Trace GitHub Copilot chat + completion detection events",
		Long: "Runs blamely's Copilot watchers (chat-session JSONL + editor/JetBrains\n" +
			"logs) against a printing sink and shows every attribution event they\n" +
			"would record — tool, gen_type (chat/completion), model, and file.\n" +
			"Nothing is written to the database.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tools.DebugCopilotLogs(cmd.Context(), os.Stdout)
		},
	}
}

func cmdLogCodex() *cobra.Command {
	return &cobra.Command{
		Use:   "codex",
		Short: "Trace Codex CLI session detection events",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tools.DebugCodexLogs(cmd.Context(), os.Stdout)
		},
	}
}

func cmdLogClaude() *cobra.Command {
	return &cobra.Command{
		Use:   "claude",
		Short: "Explain how to trace Claude Code attribution (hook-driven)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tools.DebugClaudeLogs(cmd.Context(), os.Stdout)
		},
	}
}

func cmdLogGemini() *cobra.Command {
	return &cobra.Command{
		Use:   "gemini",
		Short: "Explain how to trace Gemini CLI attribution (hook-driven)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tools.DebugGeminiLogs(cmd.Context(), os.Stdout)
		},
	}
}

func cmdLogCursor() *cobra.Command {
	var debug bool
	c := &cobra.Command{
		Use:   "cursor",
		Short: "Tail Cursor logs and show detected AI-apply events",
		Long: "Watches Cursor's extension-host log files and prints every line that\n" +
			"blamely's CursorLogWatcher would record as a Composer/Agent apply event.\n\n" +
			"Use --debug to see every scanned line (with [MATCH] or [skip] prefix)\n" +
			"so you can trace why a specific Composer action was or was not detected.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return tools.DebugCursorLogs(cmd.Context(), debug, os.Stdout)
		},
	}
	c.Flags().BoolVar(&debug, "debug", false, "show all scanned lines, not just matches")
	return c
}

func cmdDoctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Self-check: daemon + per-tool hooks + git hook + PATH + binary + DB",
		Long: "Read-only self-check that prints what's wired up correctly and what's\n" +
			"not. Mirrors the output style of `brew doctor` / `flutter doctor`.\n" +
			"Does NOT modify anything — fix recommendations are printed at the end.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return install.Doctor(os.Stdout)
		},
	}
}

// cmdConfig manages ~/.blamely/config.json — the toggles that decide what each
// commit's git note includes (file detail, conversation, message, tokens, …).
// With no subcommand it prints the current settings.
func cmdConfig() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "View or change what blamely writes into commit notes",
		Long: "Manage ~/.blamely/config.json. Every toggle defaults to on; turning one\n" +
			"off keeps it out of future commit notes (existing notes are untouched).\n\n" +
			"Keys: " + strings.Join(config.NoteKeys(), ", ") + "\n\n" +
			"Examples:\n" +
			"  blamely config                       # show current settings\n" +
			"  blamely config get note.conversation\n" +
			"  blamely config set note.file_lines off\n" +
			"  blamely config set tokens true       # 'note.' prefix optional\n" +
			"  blamely config path",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd)
		},
	}
	c.AddCommand(cmdConfigShow(), cmdConfigGet(), cmdConfigSet(), cmdConfigPath())
	return c
}

func cmdConfigShow() *cobra.Command {
	return &cobra.Command{
		Use:     "show",
		Aliases: []string{"list", "ls"},
		Short:   "Print the current settings",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd)
		},
	}
}

func runConfigShow(cmd *cobra.Command) error {
	cfg := config.LoadConfig()
	if path, err := config.ConfigFile(); err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n", path)
	}
	for _, key := range config.NoteKeys() {
		v, _ := cfg.GetBool(key)
		fmt.Fprintf(cmd.OutOrStdout(), "  %-30s %v\n", key, v)
	}
	return nil
}

func cmdConfigGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one setting's value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadConfig()
			v, ok := cfg.GetBool(args[0])
			if !ok {
				return fmt.Errorf("unknown key %q (valid: %s)", args[0], strings.Join(config.NoteKeys(), ", "))
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
}

func cmdConfigSet() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <true|false>",
		Short: "Change a setting and save it to ~/.blamely/config.json",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			val, err := config.ParseBoolValue(args[1])
			if err != nil {
				return fmt.Errorf("invalid value %q: want true/false (also on/off, yes/no)", args[1])
			}
			cfg := config.LoadConfig()
			if !cfg.SetBool(args[0], val) {
				return fmt.Errorf("unknown key %q (valid: %s)", args[0], strings.Join(config.NoteKeys(), ", "))
			}
			path, err := config.SaveConfig(cfg)
			if err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s = %v  (%s)\n", canonicalKey(args[0]), val, path)
			return nil
		},
	}
}

// canonicalKey maps a user-supplied key (bare or dotted, any case) to its
// canonical dotted form so `set` echoes a consistent name. Falls back to the
// input if it's not a known key (Set already rejected it by then).
func canonicalKey(userKey string) string {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(userKey)), "note.")
	for _, k := range config.NoteKeys() {
		if strings.TrimPrefix(k, "note.") == norm {
			return k
		}
	}
	return userKey
}

func cmdConfigPath() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the path to the config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.ConfigFile()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}
