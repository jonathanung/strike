package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/internal/product/auth"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
)

// localFilesResponse is GET /v1/local-files. Paths only: no file contents,
// no bearer token, no credentials. Telemetry is false because strike serve
// has no telemetry export (bootstrap capabilities.telemetry stays false).
type localFilesResponse struct {
	Telemetry  bool            `json:"telemetry"`
	Auth       string          `json:"auth"`
	WorkDir    string          `json:"workDir"`
	GlobalRoot string          `json:"globalRoot,omitempty"`
	ProjectKey string          `json:"projectKey,omitempty"`
	Note       string          `json:"note"`
	Files      []localFileJSON `json:"files"`
}

type localFileJSON struct {
	Path   string `json:"path"`
	Group  string `json:"group"`
	Access string `json:"access"`
	Detail string `json:"detail"`
	Exists bool   `json:"exists"`
}

type stateFile struct {
	path   string
	detail string
}

func (s *Server) handleLocalFiles(w http.ResponseWriter, r *http.Request) {
	workDir, err := os.Getwd()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	instructionRoot := workDir
	projectKey := ""
	if id, err := project.Resolve(r.Context(), workDir); err == nil {
		if id.Root != "" {
			instructionRoot = id.Root
		}
		projectKey = id.Key
	}

	files := make([]localFileJSON, 0, 48)
	for _, rd := range config.ServeReads(workDir, instructionRoot) {
		files = append(files, stampLocalFile(rd.Path, rd.Group, rd.Access, rd.Detail))
	}
	files = append(files, stampLocalFile(s.opts.SessionDir, "state", "created-at-startup", "Session JSONL logs for this process."))
	if root := config.GlobalRoot(); root != "" && projectKey != "" {
		for _, st := range projectStateFiles(root, projectKey) {
			files = append(files, stampLocalFile(st.path, "state", "created-at-startup", st.detail))
		}
	}
	if s.opts.Workspaces != nil {
		files = append(files, stampLocalFile(s.opts.Workspaces.Path(), "state", "read-if-present", "Workspace registry. The file is created on the first write."))
	}
	if dir := auditDir(s.opts.Audit); dir != "" {
		files = append(files, stampLocalFile(dir, "state", "created-at-startup", "Append-only audit segments. A serve op record stores the op type and source IP, not the op body."))
	}
	files = append(files, stampLocalFile(auth.DefaultPath(), "credentials", "read-if-present", "Provider credentials. Read at startup. Contents are not returned."))

	authMode := "off"
	switch {
	case s.opts.Auth && strings.TrimSpace(s.opts.TokenFile) != "":
		authMode = "file"
		files = append(files, stampLocalFile(s.opts.TokenFile, "credentials", "read-each-request", "Bearer token file. Re-read on each /v1 check. The secret is not returned."))
	case s.opts.Auth:
		authMode = "flag"
	}

	writeJSON(w, http.StatusOK, localFilesResponse{
		Telemetry:  false,
		Auth:       authMode,
		WorkDir:    workDir,
		GlobalRoot: config.GlobalRoot(),
		ProjectKey: projectKey,
		Note:       "No telemetry is sent. This list is the files this process reads or creates. Optional files that are missing are skipped. File contents are not included. ~/.strike/onboarding.json is the TUI first-run record and is not read by strike serve.",
		Files:      dedupeLocalFiles(files),
	})
}

func stampLocalFile(path, group, access, detail string) localFileJSON {
	if abs, err := filepath.Abs(path); err == nil && abs != "" {
		path = abs
	}
	_, err := os.Stat(path)
	return localFileJSON{
		Path:   path,
		Group:  group,
		Access: access,
		Detail: detail,
		Exists: err == nil,
	}
}

func dedupeLocalFiles(in []localFileJSON) []localFileJSON {
	seen := make(map[string]struct{}, len(in))
	out := make([]localFileJSON, 0, len(in))
	for _, f := range in {
		if f.Path == "" {
			continue
		}
		if _, ok := seen[f.Path]; ok {
			continue
		}
		seen[f.Path] = struct{}{}
		out = append(out, f)
	}
	return out
}

// projectStateFiles are the per-project stores assemble opens under the
// global root. The names match memory/history/issue/plan/artifact/ledger/goal
// Open. localfiles_test.go fails if those Open paths drift.
func projectStateFiles(globalRoot, projectKey string) []stateFile {
	sum := sha256.Sum256([]byte(projectKey))
	hexName := hex.EncodeToString(sum[:])
	return []stateFile{
		{filepath.Join(globalRoot, "history", hexName+".jsonl"), "Prompt history for this project. One JSONL file, created at startup."},
		{filepath.Join(globalRoot, "memory", hexName+".json"), "Project memory. One JSON file, created at startup."},
		{filepath.Join(globalRoot, "issues", hexName+".json"), "Project issues. One JSON file, created at startup."},
		{filepath.Join(globalRoot, "plans", hexName+".json"), "Project plans. One JSON file, created at startup."},
		{filepath.Join(globalRoot, "artifacts", hexName+".json"), "Project artifacts. One JSON file, created at startup."},
		{filepath.Join(globalRoot, "ledger", hexName+".json"), "Project decision ledger. One JSON file, created at startup."},
		{filepath.Join(globalRoot, "goals", hexName), "Project goals directory. Holds goals.json, intents.json, and iteration logs. Created at startup."},
	}
}

type dirReporter interface {
	Dir() string
}

func auditDir(a OpAuditor) string {
	if a == nil {
		return ""
	}
	if d, ok := a.(dirReporter); ok {
		return d.Dir()
	}
	return ""
}
