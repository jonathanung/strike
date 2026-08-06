package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type applyPatchTool struct{}

func NewApplyPatch() Tool { return applyPatchTool{} }

func (applyPatchTool) Name() string { return "apply_patch" }

func (applyPatchTool) Contract() Contract {
	return staticContract(SideEffectWorkspaceMutative, IdempotencyConditional)
}

func (applyPatchTool) Description() string {
	return `Apply a multi-file patch in a stripped-down, file-oriented diff format.

Prefer this for coordinated multi-file edits (especially GPT-style patches). Prefer edit for a single exact string replacement; prefer write only for brand-new files.

Envelope:
*** Begin Patch
[one or more file operations]
*** End Patch

Operations:
*** Add File: <path>
+line
+line
*** Delete File: <path>
*** Update File: <path>
*** Move to: <newpath>   (optional, immediately after Update File)
@@ optional context anchor
 context line (leading space)
-old line
+new line
*** End Patch

Rules:
  - Include Begin/End markers and an action header per file.
  - Prefix every added line with + (including Add File bodies).
  - Update hunks use exact context match; the tool fails if old lines are not found.
  - One permission prompt covers all paths in the patch; nothing is written until the whole patch validates.`
}

func (applyPatchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"patch": {"type": "string", "description": "Full patch text including *** Begin Patch / *** End Patch markers"},
			"baseHashes": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Optional map of path → sha256 hex of expected content before apply; fails with precondition_failed on mismatch"}
		},
		"required": ["patch"]
	}`)
}

type applyPatchArgs struct {
	Patch      string            `json:"patch"`
	BaseHashes map[string]string `json:"baseHashes"`
}

// patchHunk is one parsed file operation from the patch envelope.
type patchHunk struct {
	Type    string // add | delete | update
	Path    string
	MoveTo  string
	Content string // add body
	Chunks  []patchChunk
}

type patchChunk struct {
	Context  string // optional @@ anchor (exact line match)
	OldLines []string
	NewLines []string
}

// plannedOp is a validated in-memory mutation ready to commit.
type plannedOp struct {
	Type    string `json:"type"` // add | delete | update | move
	Path    string `json:"path"`
	MoveTo  string `json:"moveTo,omitempty"`
	AbsPath string `json:"-"`
	AbsMove string `json:"-"`
	Content string `json:"-"` // new file contents (add/update/move)
	RelPath string `json:"-"`
	RelMove string `json:"-"`
}

func (applyPatchTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a applyPatchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	if strings.TrimSpace(a.Patch) == "" {
		return Result{}, ErrInvalidArgs("patch is required")
	}

	tempDir := ""
	if tc != nil {
		tempDir = tc.SessionTempDir
	}
	planned, originals, err := preparePatch(tc.WorkDir, tempDir, a.Patch)
	if err != nil {
		return Result{}, err
	}

	// Freshness + optional baseHash preconditions before permission/claim.
	if err := checkPatchPreconditions(tc, planned, originals, a.BaseHashes); err != nil {
		return Result{}, err
	}

	// Collect unique relative paths for one edit permission ask.
	seen := make(map[string]struct{})
	var patterns []string
	addPat := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		patterns = append(patterns, p)
	}
	for _, op := range planned {
		addPat(op.RelPath)
		addPat(op.RelMove)
	}

	meta, _ := json.Marshal(plannedOpsMeta(planned))
	if err := tc.Ask(ctx, AskRequest{
		Permission: "edit",
		Patterns:   patterns,
		Always:     []string{"*"},
		Metadata:   meta,
	}); err != nil {
		return Result{}, err
	}

	// Claim every path before commit so block policy can refuse the whole patch.
	var overlapWarns []string
	seenClaim := make(map[string]struct{})
	claim := func(abs, rel string) error {
		if abs == "" {
			return nil
		}
		if _, ok := seenClaim[abs]; ok {
			return nil
		}
		seenClaim[abs] = struct{}{}
		w, err := tc.ClaimWrite(abs, rel)
		if err != nil {
			return err
		}
		if w != "" {
			overlapWarns = append(overlapWarns, w)
		}
		return nil
	}
	for _, op := range planned {
		if err := claim(op.AbsPath, op.RelPath); err != nil {
			return Result{}, err
		}
		if err := claim(op.AbsMove, op.RelMove); err != nil {
			return Result{}, err
		}
	}

	// Re-check content race immediately before commit (plan-time originals).
	if err := checkPatchContentUnchanged(originals); err != nil {
		return Result{}, err
	}
	if err := checkPatchPreconditions(tc, planned, originals, a.BaseHashes); err != nil {
		return Result{}, err
	}

	for abs := range originals {
		tc.SnapshotPath(abs)
	}
	// Capture existence before commit for turn-diff kinds.
	existed := make(map[string]bool, len(planned)*2)
	for _, op := range planned {
		if op.AbsPath != "" {
			existed[op.AbsPath] = FileExisted(op.AbsPath)
		}
		if op.AbsMove != "" {
			existed[op.AbsMove] = FileExisted(op.AbsMove)
		}
	}
	if err := commitPatchOps(tc.WorkDir, tempDir, planned, originals); err != nil {
		return Result{}, err
	}
	notePatchTurnChanges(tc, planned, existed)
	// Sync all files first, then one diagnostics collect (single block, not N).
	diagPaths := notifyPatchFileSync(tc, planned)

	out := patchSuccessSummary(planned)
	for _, w := range overlapWarns {
		out = AppendOverlapWarning(out, w)
	}
	res := Result{
		Title:    fmt.Sprintf("%d file(s)", len(planned)),
		Output:   out,
		Metadata: meta,
	}
	return tc.AppendDiagnostics(ctx, res, diagPaths...), nil
}

// checkPatchPreconditions enforces FileState freshness and optional baseHashes
// for every path the patch will mutate.
func checkPatchPreconditions(tc *Context, planned []plannedOp, originals map[string]pathOriginal, baseHashes map[string]string) error {
	if tc == nil {
		return nil
	}
	seen := make(map[string]struct{})
	checkPath := func(abs, rel string) error {
		if abs == "" {
			return nil
		}
		if _, ok := seen[abs]; ok {
			return nil
		}
		seen[abs] = struct{}{}
		if orig, ok := originals[abs]; ok && orig.exists {
			if err := tc.Files.CheckFresh(abs, rel); err != nil {
				return err
			}
		}
		if len(baseHashes) == 0 {
			return nil
		}
		// Match baseHashes by relative path (slash) or basename keys.
		want := baseHashFor(baseHashes, rel, abs)
		if want == "" {
			return nil
		}
		return CheckBaseHash(abs, want, rel)
	}
	for _, op := range planned {
		if err := checkPath(op.AbsPath, op.RelPath); err != nil {
			return err
		}
		if err := checkPath(op.AbsMove, op.RelMove); err != nil {
			return err
		}
	}
	return nil
}

func baseHashFor(m map[string]string, rel, abs string) string {
	if m == nil {
		return ""
	}
	// Exact relative (slash-normalized) or absolute keys only — never basename
	// alone, which would cross-apply hashes across same-named paths in different dirs.
	if v, ok := m[rel]; ok {
		return v
	}
	if slash := filepath.ToSlash(rel); slash != rel {
		if v, ok := m[slash]; ok {
			return v
		}
	}
	if v, ok := m[abs]; ok {
		return v
	}
	if clean := filepath.Clean(abs); clean != abs {
		if v, ok := m[clean]; ok {
			return v
		}
	}
	return ""
}

func checkPatchContentUnchanged(originals map[string]pathOriginal) error {
	for abs, orig := range originals {
		if !orig.exists {
			// Must still be missing (not created by a concurrent writer).
			if FileExisted(abs) {
				return PreconditionFailed(fmt.Sprintf("%s changed concurrently (appeared before apply); re-read before editing", abs))
			}
			continue
		}
		if err := CheckContentUnchanged(abs, orig.data, filepath.Base(abs)); err != nil {
			return err
		}
	}
	return nil
}

func notePatchTurnChanges(tc *Context, planned []plannedOp, existed map[string]bool) {
	if tc == nil {
		return
	}
	for _, op := range planned {
		switch op.Type {
		case "delete":
			tc.NoteTurnChange(op.AbsPath, true, true)
		case "move":
			tc.NoteTurnChange(op.AbsPath, true, true)
			if op.AbsMove != "" {
				tc.NoteTurnChange(op.AbsMove, existed[op.AbsMove], false)
			}
		case "add":
			tc.NoteTurnChange(op.AbsPath, existed[op.AbsPath], false)
		default: // update
			tc.NoteTurnChange(op.AbsPath, true, false)
		}
	}
}

// notifyPatchFileSync drives LSP (or similar) document sync after a successful patch.
// Returns absolute paths of non-deleted files for a single diagnostics collect.
func notifyPatchFileSync(tc *Context, planned []plannedOp) []string {
	if tc == nil {
		return nil
	}
	var diagPaths []string
	for _, op := range planned {
		switch op.Type {
		case "delete":
			tc.NotifyFileSync(op.AbsPath, "", true)
		case "move":
			tc.NotifyFileSync(op.AbsPath, "", true)
			if op.AbsMove != "" {
				tc.NotifyFileSync(op.AbsMove, op.Content, false)
				diagPaths = append(diagPaths, op.AbsMove)
			}
		default:
			// add / update
			path := op.AbsPath
			if op.AbsMove != "" {
				path = op.AbsMove
			}
			tc.NotifyFileSync(path, op.Content, false)
			diagPaths = append(diagPaths, path)
		}
	}
	return diagPaths
}

// ApplyPatchToWorkDir validates and commits a multi-file patch under workDir
// without permission prompts. Used by host-side diff-viewer apply. On commit
// failure, rolls back so partial state is avoided when possible.
func ApplyPatchToWorkDir(workDir, patch string) (summary string, err error) {
	planned, originals, err := preparePatch(workDir, "", patch)
	if err != nil {
		return "", err
	}
	if err := commitPatchOps(workDir, "", planned, originals); err != nil {
		return "", err
	}
	return patchSuccessSummary(planned), nil
}

func preparePatch(workDir, tempDir, patch string) ([]plannedOp, map[string]pathOriginal, error) {
	if strings.TrimSpace(patch) == "" {
		return nil, nil, fmt.Errorf("patch is required")
	}
	hunks, err := parsePatch(patch)
	if err != nil {
		return nil, nil, err
	}
	if len(hunks) == 0 {
		return nil, nil, fmt.Errorf("apply_patch: no file operations in patch")
	}
	return planPatchOps(workDir, tempDir, hunks)
}

func patchSuccessSummary(planned []plannedOp) string {
	var summary []string
	for _, op := range planned {
		switch op.Type {
		case "add":
			summary = append(summary, "A "+op.RelPath)
		case "delete":
			summary = append(summary, "D "+op.RelPath)
		case "move":
			summary = append(summary, "R "+op.RelPath+" -> "+op.RelMove)
		default:
			summary = append(summary, "M "+op.RelPath)
		}
	}
	return "Success. Updated the following files:\n" + strings.Join(summary, "\n")
}

func plannedOpsMeta(ops []plannedOp) []map[string]any {
	out := make([]map[string]any, len(ops))
	for i, op := range ops {
		m := map[string]any{
			"type": op.Type,
			"path": op.RelPath,
		}
		if op.RelMove != "" {
			m["moveTo"] = op.RelMove
		}
		out[i] = m
	}
	return out
}

func parsePatch(text string) ([]patchHunk, error) {
	// Normalize newlines; keep content lines as-is otherwise.
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	const beginMarker = "*** Begin Patch"
	const endMarker = "*** End Patch"

	beginIdx := -1
	endIdx := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == beginMarker {
			beginIdx = i
		}
		if trim == endMarker {
			endIdx = i
		}
	}
	if beginIdx < 0 || endIdx < 0 || beginIdx >= endIdx {
		return nil, fmt.Errorf("apply_patch: invalid patch format: missing *** Begin Patch / *** End Patch markers")
	}

	var hunks []patchHunk
	i := beginIdx + 1
	for i < endIdx {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			if path == "" {
				return nil, fmt.Errorf("apply_patch: Add File missing path")
			}
			i++
			var body []string
			for i < endIdx && !strings.HasPrefix(lines[i], "***") {
				l := lines[i]
				if strings.HasPrefix(l, "+") {
					body = append(body, strings.TrimPrefix(l, "+"))
				} else if strings.TrimSpace(l) == "" {
					// allow blank separators
				} else {
					return nil, fmt.Errorf("apply_patch: Add File %q: expected + lines, got %q", path, l)
				}
				i++
			}
			content := strings.Join(body, "\n")
			if len(body) > 0 {
				content += "\n"
			}
			hunks = append(hunks, patchHunk{Type: "add", Path: path, Content: content})

		case strings.HasPrefix(line, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if path == "" {
				return nil, fmt.Errorf("apply_patch: Delete File missing path")
			}
			hunks = append(hunks, patchHunk{Type: "delete", Path: path})
			i++

		case strings.HasPrefix(line, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			if path == "" {
				return nil, fmt.Errorf("apply_patch: Update File missing path")
			}
			i++
			moveTo := ""
			if i < endIdx && strings.HasPrefix(lines[i], "*** Move to:") {
				moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to:"))
				if moveTo == "" {
					return nil, fmt.Errorf("apply_patch: Move to missing path")
				}
				i++
			}
			chunks, next, err := parseUpdateChunks(lines, i, endIdx)
			if err != nil {
				return nil, err
			}
			hunks = append(hunks, patchHunk{Type: "update", Path: path, MoveTo: moveTo, Chunks: chunks})
			i = next

		case strings.TrimSpace(line) == "":
			i++
		default:
			return nil, fmt.Errorf("apply_patch: unexpected line in patch: %q", line)
		}
	}
	return hunks, nil
}

func parseUpdateChunks(lines []string, start, end int) ([]patchChunk, int, error) {
	var chunks []patchChunk
	i := start
	for i < end && !strings.HasPrefix(lines[i], "***") {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		ctx := ""
		if strings.HasPrefix(lines[i], "@@") {
			ctx = strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))
			i++
		} else if !isHunkBodyLine(lines[i]) {
			return nil, i, fmt.Errorf("apply_patch: expected @@ or hunk body line, got %q", lines[i])
		}
		var oldLines, newLines []string
		for i < end && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "***") {
			l := lines[i]
			if l == "*** End of File" {
				i++
				break
			}
			if !isHunkBodyLine(l) {
				// blank line ends an implicit (no-@@) chunk only when we already
				// collected body; otherwise skip leading blanks.
				if strings.TrimSpace(l) == "" {
					if len(oldLines) == 0 && len(newLines) == 0 {
						i++
						continue
					}
					break
				}
				return nil, i, fmt.Errorf("apply_patch: invalid hunk line %q (want space/-/+ prefix)", l)
			}
			if len(l) == 0 {
				oldLines = append(oldLines, "")
				newLines = append(newLines, "")
				i++
				continue
			}
			switch l[0] {
			case ' ':
				s := l[1:]
				oldLines = append(oldLines, s)
				newLines = append(newLines, s)
			case '-':
				oldLines = append(oldLines, l[1:])
			case '+':
				newLines = append(newLines, l[1:])
			}
			i++
		}
		if len(oldLines) == 0 && len(newLines) == 0 && ctx == "" {
			continue
		}
		chunks = append(chunks, patchChunk{Context: ctx, OldLines: oldLines, NewLines: newLines})
	}
	return chunks, i, nil
}

func isHunkBodyLine(l string) bool {
	if l == "" {
		// empty line can be a context line with empty content (rare)
		return true
	}
	switch l[0] {
	case ' ', '-', '+':
		return true
	default:
		return false
	}
}

// pathOriginal is the on-disk state of a path at first touch during plan,
// used for best-effort rollback if commit fails mid-way.
type pathOriginal struct {
	exists bool
	data   []byte
}

func planPatchOps(workDir, tempDir string, hunks []patchHunk) ([]plannedOp, map[string]pathOriginal, error) {
	out := make([]plannedOp, 0, len(hunks))
	// working holds the latest planned content per abs path so multiple Update
	// ops on the same file chain rather than re-reading disk each time.
	working := make(map[string]string)
	originals := make(map[string]pathOriginal)

	captureOriginal := func(abs string) error {
		if _, ok := originals[abs]; ok {
			return nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				originals[abs] = pathOriginal{exists: false}
				return nil
			}
			return err
		}
		originals[abs] = pathOriginal{exists: true, data: data}
		return nil
	}

	for _, h := range hunks {
		abs, rel, err := resolveAllowedPath(workDir, tempDir, h.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("apply_patch: %w", err)
		}
		switch h.Type {
		case "add":
			if err := captureOriginal(abs); err != nil {
				return nil, nil, fmt.Errorf("apply_patch: Add File %q: %w", rel, err)
			}
			if _, ok := working[abs]; ok {
				return nil, nil, fmt.Errorf("apply_patch: Add File %q: already exists", rel)
			}
			if originals[abs].exists {
				return nil, nil, fmt.Errorf("apply_patch: Add File %q: already exists", rel)
			}
			working[abs] = h.Content
			out = append(out, plannedOp{
				Type:    "add",
				Path:    h.Path,
				AbsPath: abs,
				RelPath: rel,
				Content: h.Content,
			})
		case "delete":
			if err := captureOriginal(abs); err != nil {
				return nil, nil, fmt.Errorf("apply_patch: Delete File %q: %w", rel, err)
			}
			if _, inWorking := working[abs]; !inWorking && !originals[abs].exists {
				return nil, nil, fmt.Errorf("apply_patch: Delete File %q: %w", rel, os.ErrNotExist)
			}
			delete(working, abs)
			out = append(out, plannedOp{
				Type:    "delete",
				Path:    h.Path,
				AbsPath: abs,
				RelPath: rel,
			})
		case "update":
			var content string
			if prev, ok := working[abs]; ok {
				content = prev
			} else {
				if err := captureOriginal(abs); err != nil {
					return nil, nil, fmt.Errorf("apply_patch: Update File %q: %w", rel, err)
				}
				if !originals[abs].exists {
					return nil, nil, fmt.Errorf("apply_patch: Update File %q: %w", rel, os.ErrNotExist)
				}
				content = string(originals[abs].data)
			}
			newContent, err := applyUpdateChunks(content, h.Chunks, rel)
			if err != nil {
				return nil, nil, err
			}
			opType := "update"
			var absMove, relMove string
			if h.MoveTo != "" {
				opType = "move"
				var moveErr error
				absMove, relMove, moveErr = resolveAllowedPath(workDir, tempDir, h.MoveTo)
				if moveErr != nil {
					return nil, nil, fmt.Errorf("apply_patch: %w", moveErr)
				}
				if absMove != abs {
					if err := captureOriginal(absMove); err != nil {
						return nil, nil, fmt.Errorf("apply_patch: Move to %q: %w", relMove, err)
					}
					if _, ok := working[absMove]; ok || originals[absMove].exists {
						return nil, nil, fmt.Errorf("apply_patch: Move to %q: already exists", relMove)
					}
				}
				delete(working, abs)
				working[absMove] = newContent
			} else {
				working[abs] = newContent
			}
			out = append(out, plannedOp{
				Type:    opType,
				Path:    h.Path,
				MoveTo:  h.MoveTo,
				AbsPath: abs,
				AbsMove: absMove,
				RelPath: rel,
				RelMove: relMove,
				Content: newContent,
			})
		default:
			return nil, nil, fmt.Errorf("apply_patch: unknown hunk type %q", h.Type)
		}
	}
	return out, originals, nil
}

func applyUpdateChunks(content string, chunks []patchChunk, rel string) (string, error) {
	if len(chunks) == 0 {
		return content, nil
	}
	// Work line-oriented without a forced trailing empty element.
	keepTrailingNL := strings.HasSuffix(content, "\n")
	body := content
	if keepTrailingNL {
		body = strings.TrimSuffix(body, "\n")
	}
	var lines []string
	if body == "" {
		lines = []string{}
	} else {
		lines = strings.Split(body, "\n")
	}

	searchFrom := 0
	for ci, chunk := range chunks {
		if chunk.Context != "" {
			found := -1
			for i := searchFrom; i < len(lines); i++ {
				if lines[i] == chunk.Context {
					found = i
					break
				}
			}
			if found < 0 {
				return "", fmt.Errorf("apply_patch: %s chunk %d: context %q not found", rel, ci, chunk.Context)
			}
			// Seek past the anchor so the following old/new lines match after it.
			searchFrom = found + 1
		}

		if len(chunk.OldLines) == 0 {
			// Pure insertion at searchFrom (or EOF if still 0 and no context).
			idx := searchFrom
			if chunk.Context == "" && searchFrom == 0 && ci == 0 {
				idx = len(lines)
			}
			newLines := make([]string, 0, len(lines)+len(chunk.NewLines))
			newLines = append(newLines, lines[:idx]...)
			newLines = append(newLines, chunk.NewLines...)
			newLines = append(newLines, lines[idx:]...)
			lines = newLines
			searchFrom = idx + len(chunk.NewLines)
			continue
		}

		idx := indexExactLines(lines, chunk.OldLines, searchFrom)
		if idx < 0 {
			return "", fmt.Errorf("apply_patch: %s chunk %d: old lines not found (exact match required):\n%s",
				rel, ci, strings.Join(chunk.OldLines, "\n"))
		}
		newLines := make([]string, 0, len(lines)-len(chunk.OldLines)+len(chunk.NewLines))
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, chunk.NewLines...)
		newLines = append(newLines, lines[idx+len(chunk.OldLines):]...)
		lines = newLines
		searchFrom = idx + len(chunk.NewLines)
	}

	out := strings.Join(lines, "\n")
	if keepTrailingNL || len(lines) > 0 {
		// Preserve original trailing newline; ensure non-empty files end with \n
		// when the original did. Empty result stays empty.
		if keepTrailingNL {
			out += "\n"
		}
	}
	return out, nil
}

func indexExactLines(haystack, needle []string, from int) int {
	if len(needle) == 0 {
		return from
	}
	if from < 0 {
		from = 0
	}
	last := len(haystack) - len(needle)
	for i := from; i <= last; i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func commitOnePatchOp(workDir, tempDir string, op plannedOp) error {
	switch op.Type {
	case "add", "update":
		// Re-validate + O_NOFOLLOW at commit (TOCTOU vs plan-time resolve).
		return allowedWriteFile(workDir, tempDir, op.AbsPath, []byte(op.Content))
	case "delete":
		// os.Remove unlinks the final component (does not follow a leaf symlink).
		return os.Remove(op.AbsPath)
	case "move":
		if err := allowedWriteFile(workDir, tempDir, op.AbsMove, []byte(op.Content)); err != nil {
			return err
		}
		return os.Remove(op.AbsPath)
	default:
		return fmt.Errorf("apply_patch: unknown planned op %q", op.Type)
	}
}

// restorePath writes original bytes back, or removes the path if it did not exist.
func restorePath(workDir, tempDir, abs string, orig pathOriginal) error {
	if !orig.exists {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return allowedWriteFile(workDir, tempDir, abs, orig.data)
}

func rollbackPatchOps(workDir, tempDir string, applied []plannedOp, originals map[string]pathOriginal) error {
	var errs []error
	for i := len(applied) - 1; i >= 0; i-- {
		op := applied[i]
		switch op.Type {
		case "add":
			if err := restorePath(workDir, tempDir, op.AbsPath, originals[op.AbsPath]); err != nil {
				errs = append(errs, err)
			}
		case "delete", "update":
			if err := restorePath(workDir, tempDir, op.AbsPath, originals[op.AbsPath]); err != nil {
				errs = append(errs, err)
			}
		case "move":
			// Remove dest first, then restore source.
			if err := restorePath(workDir, tempDir, op.AbsMove, originals[op.AbsMove]); err != nil {
				errs = append(errs, err)
			}
			if err := restorePath(workDir, tempDir, op.AbsPath, originals[op.AbsPath]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func commitPatchOps(workDir, tempDir string, ops []plannedOp, originals map[string]pathOriginal) error {
	var applied []plannedOp
	for _, op := range ops {
		if err := commitOnePatchOp(workDir, tempDir, op); err != nil {
			// Include the failed op so partial effects (e.g. move wrote dest
			// but failed to remove source) are best-effort undone too.
			toRoll := make([]plannedOp, len(applied)+1)
			copy(toRoll, applied)
			toRoll[len(applied)] = op
			if rbErr := rollbackPatchOps(workDir, tempDir, toRoll, originals); rbErr != nil {
				return fmt.Errorf("apply_patch: commit failed: %v; rollback also failed: %v (partial state)", err, rbErr)
			}
			return fmt.Errorf("apply_patch: commit failed: %w (rolled back)", err)
		}
		applied = append(applied, op)
	}
	return nil
}
