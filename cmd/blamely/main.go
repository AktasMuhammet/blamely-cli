package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"time"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitnotes"
	"github.com/blamely/blamely/internal/install"
	"github.com/blamely/blamely/internal/report"
	"github.com/blamely/blamely/internal/store"
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
	root.AddCommand(cmdStats())
	root.AddCommand(cmdHistory())
	root.AddCommand(cmdStatus())
	root.AddCommand(cmdDoctor())

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
			daemon.Watchers = []daemon.Watcher{
				&tools.CodexWatcher{},
				&tools.CursorWatcher{},
				&tools.CursorLogWatcher{},
				&tools.CopilotWatcher{},
				&tools.CopilotChatWatcher{},
				&tools.CopilotLogWatcher{},
			}
			// DB-backed watchers go through the factory hook so daemon doesn't
			// import the tools package directly.
			daemon.DBWatcherFactory = func(db *store.DB) daemon.Watcher {
				return &tools.VelocityWatcher{DB: db}
			}
			daemon.DBWatcherFactories = append(daemon.DBWatcherFactories,
				func(db *store.DB) daemon.Watcher { return &tools.HumanEditWatcher{DB: db} },
			)
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
	return &cobra.Command{
		Use:   "install",
		Short: "Install Blamely (Claude hook, global git hook, daemon agent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return install.Run()
		},
	}
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
