package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

func newListServer(t *testing.T, dir string) *Server {
	t.Helper()
	srv, err := New(Options{SessionDir: dir, Auth: true, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func listDo(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSessionListOrgRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := session.NewManager(dir)
	root, err := m.Create(session.CreateOptions{Title: "Fix the login redirect"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Append(root.ID, protocol.UserMessage{Text: "Fix the login redirect please"}); err != nil {
		t.Fatal(err)
	}
	child, err := m.Create(session.CreateOptions{ParentSessionID: root.ID, Title: "scribe"})
	if err != nil {
		t.Fatal(err)
	}
	_ = child

	srv := newListServer(t, dir)
	unauth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/v1/session-list", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("auth = %d", unauth.Code)
	}

	created := listDo(t, srv, http.MethodPost, "/v1/session-list/folders", `{"name":"  Reviews  "}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var folder sessionFolder
	if err := json.Unmarshal(created.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}
	if folder.Name != "Reviews" || !strings.HasPrefix(folder.ID, "fld-") {
		t.Fatalf("folder = %+v", folder)
	}
	dup := listDo(t, srv, http.MethodPost, "/v1/session-list/folders", `{"name":"reviews"}`)
	if dup.Code != http.StatusBadRequest || !strings.Contains(dup.Body.String(), "already exists") {
		t.Fatalf("dup = %d %s", dup.Code, dup.Body.String())
	}
	empty := listDo(t, srv, http.MethodPost, "/v1/session-list/folders", `{"name":"   "}`)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty name = %d", empty.Code)
	}

	moved := listDo(t, srv, http.MethodPost, "/v1/session-list/bulk", `{"ids":["`+root.ID+`"],"op":"move","folderId":"`+folder.ID+`"}`)
	if moved.Code != http.StatusOK {
		t.Fatalf("move %d %s", moved.Code, moved.Body.String())
	}
	pinned := listDo(t, srv, http.MethodPatch, "/v1/session-list/sessions/"+root.ID, `{"pinned":true}`)
	if pinned.Code != http.StatusOK {
		t.Fatalf("pin %d %s", pinned.Code, pinned.Body.String())
	}
	archived := listDo(t, srv, http.MethodPost, "/v1/session-list/bulk", `{"ids":["`+root.ID+`"],"op":"archive","folderId":""}`)
	if archived.Code != http.StatusOK {
		t.Fatalf("archive %d %s", archived.Code, archived.Body.String())
	}
	badMove := listDo(t, srv, http.MethodPost, "/v1/session-list/bulk", `{"ids":["`+root.ID+`"],"op":"move","folderId":"fld-missing"}`)
	if badMove.Code != http.StatusNotFound {
		t.Fatalf("bad move %d %s", badMove.Code, badMove.Body.String())
	}
	badID := listDo(t, srv, http.MethodPatch, "/v1/session-list/sessions/foo..bar", `{"pinned":true}`)
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("bad id %d %s", badID.Code, badID.Body.String())
	}

	// A new server process reads the same file.
	srv = newListServer(t, dir)
	got := listDo(t, srv, http.MethodGet, "/v1/session-list", "")
	if got.Code != http.StatusOK {
		t.Fatalf("get %d %s", got.Code, got.Body.String())
	}
	var snap sessionListSnapshot
	if err := json.Unmarshal(got.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Folders) != 1 || snap.Folders[0].Name != "Reviews" {
		t.Fatalf("folders = %+v", snap.Folders)
	}
	org := snap.Sessions[root.ID]
	if !org.Pinned || !org.Archived || org.FolderID != folder.ID {
		t.Fatalf("org = %+v", org)
	}
	if snap.Titles[root.ID] != "Fix the login redirect" {
		t.Fatalf("title = %q", snap.Titles[root.ID])
	}
	if len(snap.Children) != 1 || snap.Children[0].ParentID != root.ID || snap.Children[0].Title != "scribe" {
		t.Fatalf("children = %+v", snap.Children)
	}

	del := listDo(t, srv, http.MethodDelete, "/v1/session-list/folders/"+folder.ID, "")
	if del.Code != http.StatusOK {
		t.Fatalf("delete %d %s", del.Code, del.Body.String())
	}
	got = listDo(t, srv, http.MethodGet, "/v1/session-list", "")
	if err := json.Unmarshal(got.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Folders) != 0 {
		t.Fatalf("folders after delete = %+v", snap.Folders)
	}
	if snap.Sessions[root.ID].FolderID != "" || !snap.Sessions[root.ID].Pinned {
		t.Fatalf("org after delete = %+v", snap.Sessions[root.ID])
	}
}

func TestSessionListSearchTitleAndRecentContent(t *testing.T) {
	dir := t.TempDir()
	m := session.NewManager(dir)
	titled, err := m.Create(session.CreateOptions{Title: "Palette polish"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Create(session.CreateOptions{ID: "abcdef1234dead", Title: "abcdef1234dead"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Append(raw.ID, protocol.UserMessage{Text: "Reload the session list after rename"}); err != nil {
		t.Fatal(err)
	}
	// A later line is not the display title, so it has to match as content.
	if err := m.Append(raw.ID, protocol.UserMessage{Text: "remember the coral bookmark"}); err != nil {
		t.Fatal(err)
	}
	long, err := m.Create(session.CreateOptions{Title: "long log"})
	if err != nil {
		t.Fatal(err)
	}
	// The match sits past the head window, so search has to read the tail.
	padding := strings.Repeat("a", listHeadLimit+8)
	if err := m.Append(long.ID, protocol.TextDelta{Text: padding + " UNIQUE_TAIL_TOKEN end"}); err != nil {
		t.Fatal(err)
	}

	srv := newListServer(t, dir)
	byTitle := listDo(t, srv, http.MethodGet, "/v1/session-list/search?q=palette", "")
	if byTitle.Code != http.StatusOK {
		t.Fatalf("title search %d %s", byTitle.Code, byTitle.Body.String())
	}
	var titleHits struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(byTitle.Body.Bytes(), &titleHits); err != nil {
		t.Fatal(err)
	}
	if len(titleHits.Hits) != 1 || titleHits.Hits[0].ID != titled.ID || titleHits.Hits[0].Where != "title" {
		t.Fatalf("title hits = %+v", titleHits.Hits)
	}

	// The first user line becomes the display title when meta is a raw id,
	// so that phrase is a title hit. A later line is content.
	byDerived := listDo(t, srv, http.MethodGet, "/v1/session-list/search?q=session%20list", "")
	var derivedHits struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(byDerived.Body.Bytes(), &derivedHits); err != nil {
		t.Fatal(err)
	}
	foundDerived := false
	for _, hit := range derivedHits.Hits {
		if hit.ID == raw.ID && hit.Where == "title" && strings.Contains(strings.ToLower(hit.Title), "session list") {
			foundDerived = true
		}
	}
	if !foundDerived {
		t.Fatalf("derived title hits = %+v", derivedHits.Hits)
	}
	byBody := listDo(t, srv, http.MethodGet, "/v1/session-list/search?q=coral%20bookmark", "")
	var bodyHits struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(byBody.Body.Bytes(), &bodyHits); err != nil {
		t.Fatal(err)
	}
	foundRaw := false
	for _, hit := range bodyHits.Hits {
		if hit.ID == raw.ID && hit.Where == "content" && strings.Contains(strings.ToLower(hit.Snippet), "coral bookmark") {
			foundRaw = true
		}
	}
	if !foundRaw {
		t.Fatalf("content hits = %+v", bodyHits.Hits)
	}

	snapRec := listDo(t, srv, http.MethodGet, "/v1/session-list", "")
	var snap sessionListSnapshot
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Titles[raw.ID] != "Reload the session list" && !strings.Contains(snap.Titles[raw.ID], "Reload the session") {
		t.Fatalf("raw id title leaked: %q", snap.Titles[raw.ID])
	}
	if snap.Titles[raw.ID] == raw.ID {
		t.Fatalf("display title is the raw id")
	}

	byTail := listDo(t, srv, http.MethodGet, "/v1/session-list/search?q=UNIQUE_TAIL_TOKEN", "")
	var tailHits struct {
		Hits []searchHit `json:"hits"`
	}
	if err := json.Unmarshal(byTail.Body.Bytes(), &tailHits); err != nil {
		t.Fatal(err)
	}
	if len(tailHits.Hits) != 1 || tailHits.Hits[0].ID != long.ID || tailHits.Hits[0].Where != "content" {
		t.Fatalf("tail hits = %+v", tailHits.Hits)
	}

	missing := listDo(t, srv, http.MethodGet, "/v1/session-list/search", "")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing q = %d", missing.Code)
	}
	unknown := listDo(t, srv, http.MethodPost, "/v1/session-list/bulk", `{"ids":["`+titled.ID+`"],"op":"nope","folderId":""}`)
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unknown bulk op") {
		t.Fatalf("bad op %d %s", unknown.Code, unknown.Body.String())
	}
}

func TestTitleIsRawID(t *testing.T) {
	id := "20060102T150405.000000000Z-ab12cd34"
	if !titleIsRawID(id, id) {
		t.Fatal("full id should be raw")
	}
	if !titleIsRawID("ab12cd34", id) {
		t.Fatal("hex prefix of the id should be raw")
	}
	if titleIsRawID("Fix the login redirect", id) {
		t.Fatal("human title flagged raw")
	}
	if titleIsRawID("deadbeef", id) && !hexishWithDigit("deadbeef") {
		t.Fatal("setup")
	}
	if titleIsRawID("Reviews", id) {
		t.Fatal("word flagged raw")
	}
}
