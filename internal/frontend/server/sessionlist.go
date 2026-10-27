package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/persist/session"
)

// Session list organisation lives in session-list.json beside the JSONL logs.
// Manager.List only reads *.jsonl, so this file is not a session.
//
// Pins, archive, and folders are list state. Folders are names the sidebar
// uses to group sessions. They are not filesystem directories and they do
// not change a session's project key.
//
// Unread is intentionally absent. The status stream (#1263) owns the server
// unread field. This file must not grow an unread key; a rewrite would drop
// any field it does not know.

const (
	sessionListVersion = 1
	sessionListFile    = "session-list.json"
	listHeadLimit      = 48 * 1024
	listTailLimit      = 96 * 1024
	listRecentCap      = 12_000
	listSearchMax      = 50
	listBulkMax        = 200
	folderNameMax      = 48
)

type sessionFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sessionOrg struct {
	Pinned   bool   `json:"pinned,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	FolderID string `json:"folderId,omitempty"`
}

type sessionListState struct {
	Version  int                   `json:"version"`
	Folders  []sessionFolder       `json:"folders"`
	Sessions map[string]sessionOrg `json:"sessions"`
}

type sessionChildBrief struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Title    string `json:"title,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

type sessionListSnapshot struct {
	Folders  []sessionFolder       `json:"folders"`
	Sessions map[string]sessionOrg `json:"sessions"`
	Previews map[string]string     `json:"previews"`
	Titles   map[string]string     `json:"titles"`
	Children []sessionChildBrief   `json:"children"`
}

type searchHit struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId,omitempty"`
	Title    string `json:"title,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Where    string `json:"where"`
}

var sessionListMu sync.Mutex

type cachedSessionText struct {
	size    int64
	modUnix int64
	preview string
	recent  string
}

var sessionTextCache struct {
	sync.Mutex
	items map[string]cachedSessionText
}

func listStatePath(dir string) string {
	return filepath.Join(dir, sessionListFile)
}

func emptyListState() sessionListState {
	return sessionListState{
		Version:  sessionListVersion,
		Folders:  []sessionFolder{},
		Sessions: map[string]sessionOrg{},
	}
}

func loadListState(dir string) (sessionListState, error) {
	data, err := os.ReadFile(listStatePath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyListState(), nil
		}
		return sessionListState{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return emptyListState(), nil
	}
	var st sessionListState
	if err := json.Unmarshal(data, &st); err != nil {
		return sessionListState{}, fmt.Errorf("session list: %w", err)
	}
	if st.Folders == nil {
		st.Folders = []sessionFolder{}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]sessionOrg{}
	}
	return st, nil
}

func saveListState(dir string, st sessionListState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st.Version = sessionListVersion
	if st.Folders == nil {
		st.Folders = []sessionFolder{}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]sessionOrg{}
	}
	// Drop rows that no longer carry any mark so the file stays small.
	for id, org := range st.Sessions {
		if !org.Pinned && !org.Archived && org.FolderID == "" {
			delete(st.Sessions, id)
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := listStatePath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func withListState(dir string, mutate func(*sessionListState) error) (sessionListState, error) {
	sessionListMu.Lock()
	defer sessionListMu.Unlock()
	st, err := loadListState(dir)
	if err != nil {
		return sessionListState{}, err
	}
	if mutate != nil {
		if err := mutate(&st); err != nil {
			return sessionListState{}, err
		}
		if err := saveListState(dir, st); err != nil {
			return sessionListState{}, err
		}
		st, err = loadListState(dir)
		if err != nil {
			return sessionListState{}, err
		}
	}
	return st, nil
}

func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	snap, err := s.sessionListSnapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) sessionListSnapshot() (sessionListSnapshot, error) {
	dir := s.opts.SessionDir
	st, err := withListState(dir, nil)
	if err != nil {
		return sessionListSnapshot{}, err
	}
	infos, err := session.NewManager(dir).List()
	if err != nil {
		return sessionListSnapshot{}, err
	}
	previews := map[string]string{}
	titles := map[string]string{}
	children := make([]sessionChildBrief, 0)
	for _, info := range infos {
		preview, _ := readSessionTexts(info.Path)
		display := displayTitle(info.Title, info.ID, preview)
		if info.ParentSessionID != "" {
			children = append(children, sessionChildBrief{
				ID:       info.ID,
				ParentID: info.ParentSessionID,
				Title:    display,
				Preview:  preview,
			})
			continue
		}
		if preview != "" {
			previews[info.ID] = preview
		}
		if display != "" {
			titles[info.ID] = display
		}
	}
	if st.Sessions == nil {
		st.Sessions = map[string]sessionOrg{}
	}
	return sessionListSnapshot{
		Folders:  st.Folders,
		Sessions: st.Sessions,
		Previews: previews,
		Titles:   titles,
		Children: children,
	}, nil
}

func (s *Server) handleSessionListFolderCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	name, err := cleanFolderName(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var created sessionFolder
	_, err = withListState(s.opts.SessionDir, func(st *sessionListState) error {
		if folderNameTaken(st.Folders, name, "") {
			return errors.New("folder already exists")
		}
		created = sessionFolder{ID: newFolderID(), Name: name}
		st.Folders = append(st.Folders, created)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleSessionListFolderRename(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	name, err := cleanFolderName(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	_, err = withListState(s.opts.SessionDir, func(st *sessionListState) error {
		idx := folderIndex(st.Folders, id)
		if idx < 0 {
			return errors.New("folder not found")
		}
		if folderNameTaken(st.Folders, name, id) {
			return errors.New("folder already exists")
		}
		st.Folders[idx].Name = name
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "folder not found" {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleSessionListFolderDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	_, err := withListState(s.opts.SessionDir, func(st *sessionListState) error {
		idx := folderIndex(st.Folders, id)
		if idx < 0 {
			return errors.New("folder not found")
		}
		st.Folders = append(st.Folders[:idx], st.Folders[idx+1:]...)
		for sid, org := range st.Sessions {
			if org.FolderID == id {
				org.FolderID = ""
				st.Sessions[sid] = org
			}
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleSessionListSessionPatch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var body struct {
		Pinned   *bool   `json:"pinned"`
		Archived *bool   `json:"archived"`
		FolderID *string `json:"folderId"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	_, err := withListState(s.opts.SessionDir, func(st *sessionListState) error {
		org := st.Sessions[id]
		if body.Pinned != nil {
			org.Pinned = *body.Pinned
		}
		if body.Archived != nil {
			org.Archived = *body.Archived
		}
		if body.FolderID != nil {
			fid := strings.TrimSpace(*body.FolderID)
			if fid != "" && folderIndex(st.Folders, fid) < 0 {
				return errors.New("folder not found")
			}
			org.FolderID = fid
		}
		st.Sessions[id] = org
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "folder not found" {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleSessionListBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs      []string `json:"ids"`
		Op       string   `json:"op"`
		FolderID string   `json:"folderId"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if len(body.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ids is empty"})
		return
	}
	if len(body.IDs) > listBulkMax {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "too many ids"})
		return
	}
	op := strings.TrimSpace(body.Op)
	switch op {
	case "pin", "unpin", "archive", "unarchive", "move":
	default:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "unknown bulk op"})
		return
	}
	for _, id := range body.IDs {
		if err := validateSessionID(id); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	_, err := withListState(s.opts.SessionDir, func(st *sessionListState) error {
		folderID := strings.TrimSpace(body.FolderID)
		if op == "move" && folderID != "" && folderIndex(st.Folders, folderID) < 0 {
			return errors.New("folder not found")
		}
		for _, id := range body.IDs {
			org := st.Sessions[id]
			switch op {
			case "pin":
				org.Pinned = true
			case "unpin":
				org.Pinned = false
			case "archive":
				org.Archived = true
			case "unarchive":
				org.Archived = false
			case "move":
				org.FolderID = folderID
			}
			st.Sessions[id] = org
		}
		return nil
	})
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "folder not found" {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleSessionListSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q is empty"})
		return
	}
	if utf8.RuneCountInString(q) > 200 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q is too long"})
		return
	}
	infos, err := session.NewManager(s.opts.SessionDir).List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	needle := strings.ToLower(q)
	hits := make([]searchHit, 0)
	for _, info := range infos {
		if len(hits) >= listSearchMax {
			break
		}
		preview, recent := readSessionTexts(info.Path)
		display := displayTitle(info.Title, info.ID, preview)
		titleHay := strings.ToLower(display + "\n" + info.Title)
		if strings.Contains(titleHay, needle) && display != "" {
			hits = append(hits, searchHit{
				ID:       info.ID,
				ParentID: info.ParentSessionID,
				Title:    display,
				Snippet:  clipSnippet(display, q),
				Where:    "title",
			})
			continue
		}
		blob := preview + "\n" + recent
		if strings.Contains(strings.ToLower(blob), needle) {
			hits = append(hits, searchHit{
				ID:       info.ID,
				ParentID: info.ParentSessionID,
				Title:    display,
				Snippet:  clipSnippet(blob, q),
				Where:    "content",
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func displayTitle(metaTitle, id, preview string) string {
	metaTitle = strings.TrimSpace(metaTitle)
	if metaTitle != "" && !titleIsRawID(metaTitle, id) {
		return metaTitle
	}
	preview = strings.TrimSpace(preview)
	if preview != "" && !titleIsRawID(preview, id) {
		return preview
	}
	return ""
}

func titleIsRawID(title, id string) bool {
	title = strings.TrimSpace(title)
	id = strings.TrimSpace(id)
	if title == "" || title == id {
		return true
	}
	if looksLikeSessionID(title) {
		return true
	}
	if id != "" && strings.HasPrefix(id, title) && len(title) >= 6 && hexishWithDigit(title) {
		return true
	}
	if len(title) >= 8 && hexishWithDigit(title) {
		return true
	}
	return false
}

func looksLikeSessionID(s string) bool {
	// 20060102T150405…Z-suffix, the shape NewID writes.
	if len(s) < 16 || s[8] != 'T' {
		return false
	}
	for _, c := range s[:8] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return strings.Contains(s, "Z")
}

func hexishWithDigit(s string) bool {
	digit := false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F', c == '-':
		default:
			return false
		}
	}
	return digit
}

func cleanFolderName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", errors.New("folder name is empty")
	}
	if utf8.RuneCountInString(name) > folderNameMax {
		return "", errors.New("folder name is too long")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("folder name has control characters")
		}
	}
	return name, nil
}

func folderNameTaken(folders []sessionFolder, name, exceptID string) bool {
	want := strings.ToLower(name)
	for _, f := range folders {
		if f.ID == exceptID {
			continue
		}
		if strings.ToLower(f.Name) == want {
			return true
		}
	}
	return false
}

func folderIndex(folders []sessionFolder, id string) int {
	for i, f := range folders {
		if f.ID == id {
			return i
		}
	}
	return -1
}

func newFolderID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fld-fallback"
	}
	return fmt.Sprintf("fld-%x", b)
}

func readSessionTexts(path string) (preview, recent string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", ""
	}
	size := st.Size()
	mod := st.ModTime().UnixNano()
	sessionTextCache.Lock()
	if sessionTextCache.items != nil {
		if c, ok := sessionTextCache.items[path]; ok && c.size == size && c.modUnix == mod {
			sessionTextCache.Unlock()
			return c.preview, c.recent
		}
	}
	sessionTextCache.Unlock()

	headN := size
	if headN > listHeadLimit {
		headN = listHeadLimit
	}
	head := make([]byte, headN)
	_, _ = io.ReadFull(f, head)
	head = trimToLastNewline(head)

	var tail []byte
	if size <= listTailLimit {
		tail = make([]byte, size)
		_, _ = f.ReadAt(tail, 0)
	} else {
		off := size - listTailLimit
		tail = make([]byte, listTailLimit)
		_, _ = f.ReadAt(tail, off)
		if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
			tail = tail[i+1:]
		}
	}
	preview, _ = textsFromJSONL(head)
	_, recent = textsFromJSONL(tail)

	sessionTextCache.Lock()
	if sessionTextCache.items == nil {
		sessionTextCache.items = map[string]cachedSessionText{}
	}
	sessionTextCache.items[path] = cachedSessionText{
		size: size, modUnix: mod, preview: preview, recent: recent,
	}
	sessionTextCache.Unlock()
	return preview, recent
}

func trimToLastNewline(b []byte) []byte {
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return b
	}
	i := bytes.LastIndexByte(b, '\n')
	if i < 0 {
		return nil
	}
	return b[:i+1]
}

func textsFromJSONL(b []byte) (firstUser, blob string) {
	if len(b) == 0 {
		return "", ""
	}
	var bld strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		text := eventText(env.Data)
		if text == "" {
			continue
		}
		if env.Type == "user.message" && firstUser == "" {
			firstUser = session.TitleFromText(text)
		}
		switch env.Type {
		case "user.message", "session.titled", "text.delta", "tool.output":
			appendCapped(&bld, text, listRecentCap)
		}
	}
	return firstUser, bld.String()
}

func eventText(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"text", "title", "data"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

// appendCapped keeps the tail of the collected text. Search is for recent
// content, so a long line should not hide the tokens at its end.
func appendCapped(b *strings.Builder, text string, cap int) {
	if text == "" {
		return
	}
	next := b.String()
	if next != "" {
		next += "\n"
	}
	next += text
	if len(next) > cap {
		next = next[len(next)-cap:]
		for len(next) > 0 && !utf8.RuneStart(next[0]) {
			next = next[1:]
		}
	}
	b.Reset()
	b.WriteString(next)
}

func clipSnippet(blob, q string) string {
	blob = strings.Join(strings.Fields(blob), " ")
	if blob == "" {
		return ""
	}
	lower := strings.ToLower(blob)
	needle := strings.ToLower(strings.TrimSpace(q))
	idx := strings.Index(lower, needle)
	if idx < 0 || idx > len(blob) {
		idx = 0
	}
	start := 0
	if idx > 40 {
		start = idx - 40
	}
	end := start + 120
	if end > len(blob) {
		end = len(blob)
	}
	// Keep the cut on byte boundaries.
	for start > 0 && !utf8.RuneStart(blob[start]) {
		start--
	}
	for end < len(blob) && !utf8.RuneStart(blob[end]) {
		end++
	}
	snip := strings.TrimSpace(blob[start:end])
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(blob) {
		snip += "…"
	}
	return snip
}
