package tools

import (
	"bytes"
	"fmt"
	"io"
)

// utf8BOM is the byte-order mark Windows tooling loves to prepend. Cursor's
// Windows hook runner pipes the PostToolUse JSON with a leading BOM, which
// json.Unmarshal rejects ("invalid character 'ï' looking for beginning of
// value") — silently dropping every hook-driven attribution on Windows
// (repro: Cursor 3.10.20 chat applies recorded as completion instead of chat).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// readHookPayload reads a hook payload from r (capped at 8 MiB) and strips a
// leading UTF-8 BOM so the JSON parses regardless of how the host editor's
// hook runner encoded the pipe.
func readHookPayload(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	return bytes.TrimPrefix(raw, utf8BOM), nil
}
