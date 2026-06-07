package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// FsEventPayload is the JSON body POSTed to /fs by the editor plugins when a
// tracked file's lifecycle changes (the editor sees these; the AI-tool hooks
// and log watchers do not). All paths are repo-relative with forward slashes —
// the same form used for file_path on /edit — so DB matching is exact.
//
// Kinds:
//
//	delete  — file removed in the IDE       → uses Path (soft-delete, recoverable)
//	create  — file created/re-created       → uses Path (clears any soft-delete)
//	rename  — file renamed or moved         → uses OldPath, NewPath
//	copy    — file duplicated from another  → uses SrcPath, DstPath
type FsEventPayload struct {
	Kind     string `json:"kind"`
	RepoPath string `json:"repo_path"`
	Path     string `json:"path,omitempty"`
	OldPath  string `json:"old_path,omitempty"`
	NewPath  string `json:"new_path,omitempty"`
	SrcPath  string `json:"src_path,omitempty"`
	DstPath  string `json:"dst_path,omitempty"`
}

func cleanRel(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "./")
}

func (s *Server) fsEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var p FsEventPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}
	if err := applyFsEvent(s.db, p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func applyFsEvent(db *store.DB, p FsEventPayload) error {
	if p.RepoPath == "" {
		return fmt.Errorf("repo_path required")
	}
	switch strings.ToLower(p.Kind) {
	case "delete":
		path := cleanRel(p.Path)
		if path == "" {
			return fmt.Errorf("delete: path required")
		}
		_, err := db.MarkFileDeleted(p.RepoPath, path, time.Now().UnixNano())
		return err
	case "create", "restore":
		path := cleanRel(p.Path)
		if path == "" {
			return fmt.Errorf("create: path required")
		}
		// A file (re)appearing at a path recovers any soft-deleted attribution
		// for it (undo/rollback). New paths with no history are a harmless no-op.
		_, err := db.RestoreFile(p.RepoPath, path)
		return err
	case "rename", "move":
		oldP, newP := cleanRel(p.OldPath), cleanRel(p.NewPath)
		if oldP == "" || newP == "" {
			return fmt.Errorf("rename: old_path and new_path required")
		}
		_, err := db.MoveFileAttribution(p.RepoPath, oldP, newP)
		return err
	case "copy":
		src, dst := cleanRel(p.SrcPath), cleanRel(p.DstPath)
		if src == "" || dst == "" {
			return fmt.Errorf("copy: src_path and dst_path required")
		}
		_, err := db.CopyFileAttribution(p.RepoPath, src, dst)
		return err
	default:
		return fmt.Errorf("unknown fs event kind %q", p.Kind)
	}
}
