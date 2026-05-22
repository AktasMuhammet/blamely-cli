package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	dirName            = ".blamely"
	dbFileName         = "db.sqlite"
	portFileName       = "daemon.port"
	stateFileName      = "state.json"
	hooksDirName       = "git-hooks"
	logFileName        = "daemon.log"
	claudeDirName      = ".claude"
	claudeSettings     = "settings.json"
	codexDirName       = ".codex"
	codexSessions      = "sessions"
	codexConfig        = "config.toml"
	cursorDirName      = ".cursor"
	cursorHooks        = "hooks.json"
	copilotDirName     = ".copilot"
	copilotHooksSubdir = "hooks"
	copilotHookFile    = "blamely.json"
	geminiDirName      = ".gemini"
	geminiSettings     = "settings.json"
)

func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return home, nil
}

func BlamelyDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dirName), nil
}

func DBPath() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, dbFileName), nil
}

func PortFile() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, portFileName), nil
}

func StateFile() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, stateFileName), nil
}

func GitHooksDir() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, hooksDirName), nil
}

func LogFile() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, logFileName), nil
}

func ClaudeSettingsPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, claudeDirName, claudeSettings), nil
}

func CodexSessionsDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, codexDirName, codexSessions), nil
}

func CodexConfigPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, codexDirName, codexConfig), nil
}

func CursorHooksPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, cursorDirName, cursorHooks), nil
}

func CopilotHooksDir() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, copilotDirName, copilotHooksSubdir), nil
}

func CopilotBlamelyHookPath() (string, error) {
	dir, err := CopilotHooksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, copilotHookFile), nil
}

func GeminiSettingsPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, geminiDirName, geminiSettings), nil
}

func EnsureBlamelyDir() (string, error) {
	d, err := BlamelyDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", d, err)
	}
	return d, nil
}
