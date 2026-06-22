package gitnotes

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/store"
)

// Test_ExtensionScenarioMatrix runs the Oobeya Blamely extension test matrix
// (project/test/Blamely_Extension_Test_Cases_Generated.md) at the buildNote
// level.
//
// The 270 documented cases are the product OS × Tool × LineType × Operation ×
// FileType. OS does not reach attribution (it's a watcher/integration concern —
// the same recorded edits produce the same note on any OS), so the
// attribution-meaningful space is Tool × LineType × Operation × FileType =
// 5 × 3 × 3 × 2 = 90 cases. Each asserts the report classification the doc
// expects on commit:
//
//	"Sadece AI"    -> "%100 AI"     (AI present, Human absent)
//	"Sadece Human" -> "%100 Human"  (Human present, AI absent)
//	"AI ve Human"  -> "AI ve Human" (both present)
func Test_ExtensionScenarioMatrix(t *testing.T) {
	tools := []store.Tool{
		store.ToolCopilot, store.ToolCursor, store.ToolClaude, store.ToolCodex, store.ToolGemini,
	}

	lineTypes := []struct {
		name           string
		aiHalf         bool // include AI lines
		humanHalf      bool // include Human lines
		wantAI, wantHu bool
	}{
		{"SadeceAI", true, false, true, false},
		{"SadeceHuman", false, true, false, true},
		{"AIveHuman", true, true, true, true},
	}

	operations := []struct {
		name         string
		doAdd, doDel bool
	}{
		{"SadeceAddition", true, false},
		{"SadeceDeletion", false, true},
		{"AdditionVeDeletion", true, true},
	}

	fileCounts := []struct {
		name string
		n    int
	}{
		{"TekDosya", 1},
		{"CokluDosya", 2},
	}

	const perFile = 4 // even, so a mixed (AI ve Human) case splits cleanly 2/2

	// isAI decides whether line i of a file is AI- or Human-authored for the
	// given line type: AI-only -> all AI, Human-only -> none, mixed -> evens.
	isAI := func(aiHalf, humanHalf bool, i int) bool {
		switch {
		case aiHalf && humanHalf:
			return i%2 == 0
		case aiHalf:
			return true
		default:
			return false
		}
	}

	for _, tool := range tools {
		for _, lt := range lineTypes {
			for _, op := range operations {
				for _, fc := range fileCounts {
					name := fmt.Sprintf("%s/%s/%s/%s", tool, lt.name, op.name, fc.name)
					t.Run(name, func(t *testing.T) {
						db := openTestDB(t)
						repo := "/r"
						now := time.Now().UnixNano()
						aiTS := now - int64(10*time.Second)

						var added []AddedLine
						deleted := map[string][]DeletedLine{}

						for f := 0; f < fc.n; f++ {
							file := fmt.Sprintf("f%d.go", f)

							if op.doAdd {
								var aiLines []store.EditLine
								for i := 0; i < perFile; i++ {
									ln := i + 1
									content := fmt.Sprintf("add_%s_f%d_l%d", tool, f, i)
									added = append(added, AddedLine{File: file, LineNum: ln, Content: content})
									if isAI(lt.aiHalf, lt.humanHalf, i) {
										aiLines = append(aiLines, store.EditLine{
											StartLine: ln, EndLine: ln,
											ContentSHA:     sha256HexStr([]byte(content)),
											ContentSHANorm: sha256HexNormStr(content),
										})
									}
								}
								if len(aiLines) > 0 {
									mustInsert(t, db, store.Edit{
										TimestampNanos: aiTS, RepoPath: repo, FilePath: file,
										Tool: tool, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
										Model: sqlNullString(string(tool) + "-model"), Lines: aiLines,
									})
								}
							}

							if op.doDel {
								var aiRemoved []store.RemovedLineHash
								var dels []DeletedLine
								for i := 0; i < perFile; i++ {
									content := fmt.Sprintf("del_%s_f%d_l%d", tool, f, i)
									dels = append(dels, DeletedLine{LineNum: 100 + i, Content: content})
									if isAI(lt.aiHalf, lt.humanHalf, i) {
										aiRemoved = append(aiRemoved, store.RemovedLineHash{
											ContentSHA: sha256HexStr([]byte(content)),
										})
									}
								}
								deleted[file] = dels
								if len(aiRemoved) > 0 {
									mustInsert(t, db, store.Edit{
										TimestampNanos: aiTS, RepoPath: repo, FilePath: file,
										Tool: tool, Confidence: store.ConfidenceHigh, GenType: store.GenTypeChat,
										Model: sqlNullString(string(tool) + "-model"), RemovedLines: aiRemoved,
									})
								}
							}
						}

						note, err := buildNote(db, repo, "sha1", now, added, deleted, nil, nil)
						if err != nil {
							t.Fatalf("buildNote: %v", err)
						}

						gotAI := note.Totals.AILines > 0 || note.Totals.AIDeletedLines > 0
						gotHu := note.Totals.HumanLines > 0 || note.Totals.HumanDeletedLines > 0
						if gotAI != lt.wantAI || gotHu != lt.wantHu {
							t.Errorf("report classification mismatch: want AI=%v Human=%v, got AI=%v Human=%v\n"+
								"  totals: AIadd=%d Hadd=%d AIdel=%d Hdel=%d  by_tool=%v",
								lt.wantAI, lt.wantHu, gotAI, gotHu,
								note.Totals.AILines, note.Totals.HumanLines,
								note.Totals.AIDeletedLines, note.Totals.HumanDeletedLines, toolKeys(note.ByTool))
						}
						// When AI is expected, the authoring tool must be named in by_tool.
						if lt.wantAI {
							if _, ok := note.ByTool[string(tool)]; !ok {
								t.Errorf("by_tool missing %q; got %v", tool, toolKeys(note.ByTool))
							}
						}
						// Every added-to file must surface (Çoklu = 2, Tek = 1).
						if op.doAdd && len(note.Files) != fc.n {
							t.Errorf("file count: want %d, got %d", fc.n, len(note.Files))
						}
					})
				}
			}
		}
	}
}

func mustInsert(t *testing.T, db *store.DB, e store.Edit) {
	t.Helper()
	if _, err := db.InsertEdit(e); err != nil {
		t.Fatal(err)
	}
}

func toolKeys(m map[string]Tool) []string {
	k := make([]string, 0, len(m))
	for t := range m {
		k = append(k, t)
	}
	sort.Strings(k)
	return k
}
