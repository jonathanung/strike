package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
)

type fakeInit struct {
	exists  bool
	path    string
	writeN  int
	forceN  int
	writeEr error
	existEr error
}

func (f *fakeInit) Exists() (bool, string, error) {
	if f.existEr != nil {
		return false, f.path, f.existEr
	}
	path := f.path
	if path == "" {
		path = filepath.Join("proj", "AGENTS.md")
	}
	return f.exists, path, nil
}

func (f *fakeInit) Write(force bool) (string, bool, error) {
	f.writeN++
	if force {
		f.forceN++
	}
	if f.writeEr != nil {
		return "", false, f.writeEr
	}
	if f.exists && !force {
		return f.path, false, host.ErrInitExists
	}
	created := !f.exists
	f.exists = true
	path := f.path
	if path == "" {
		path = filepath.Join("proj", "AGENTS.md")
	}
	return path, created, nil
}

func TestInitInCommandCatalog(t *testing.T) {
	found := false
	for _, spec := range builtinCommandSpecs {
		if spec.ID == commandInit && spec.Name == "/init" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("/init missing from builtinCommandSpecs")
	}
	if _, ok := reservedCommandNames["init"]; !ok {
		t.Fatal("init not reserved")
	}
	if validSkillName("init") {
		t.Fatal("init should not be a valid skill name")
	}
}

func TestInitCreatesWhenMissing(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fi := &fakeInit{path: "/tmp/proj/AGENTS.md"}
	m.services.Init = fi

	next, cmd := m.handleCommand("/init")
	nm := next.(Model)
	if nm.modal != nil {
		t.Fatal("unexpected modal on create path")
	}
	if cmd == nil {
		t.Fatal("expected write cmd")
	}
	msg := cmd()
	res, ok := msg.(initResultMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if res.err != "" || !res.created || res.path != "/tmp/proj/AGENTS.md" {
		t.Fatalf("result = %+v", res)
	}
	if fi.writeN != 1 || fi.forceN != 0 {
		t.Fatalf("writeN=%d forceN=%d", fi.writeN, fi.forceN)
	}

	next, _ = nm.applyInitResult(res)
	nm = next.(Model)
	if !strings.Contains(nm.notice, "created AGENTS.md") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestInitConfirmsWhenExists(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fi := &fakeInit{exists: true, path: "/tmp/proj/AGENTS.md"}
	m.services.Init = fi

	next, cmd := m.handleCommand("/init")
	if cmd != nil {
		t.Fatal("unexpected cmd before confirm")
	}
	nm := next.(Model)
	modal, ok := nm.modal.(*initConfirmModal)
	if !ok {
		t.Fatalf("modal = %T, want initConfirmModal", nm.modal)
	}

	// Cancel via esc.
	var closeCmd tea.Cmd
	nm.modal, closeCmd = modal.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if nm.modal != nil {
		t.Fatal("modal should close on esc")
	}
	if closeCmd == nil {
		t.Fatal("expected cancel result cmd")
	}
	if res := closeCmd().(initResultMsg); !res.canceled {
		t.Fatalf("result = %+v", res)
	}
}

func TestInitConfirmReplace(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fi := &fakeInit{exists: true, path: "/work/AGENTS.md"}
	m.services.Init = fi
	m.modal = newInitConfirmModal(fi.path, fi)

	modal := m.modal.(*initConfirmModal)
	modal.choice = 0
	var cmd tea.Cmd
	m.modal, cmd = modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modal != nil {
		t.Fatal("modal should close after replace")
	}
	if cmd == nil {
		t.Fatal("expected write cmd")
	}
	res := cmd().(initResultMsg)
	if res.err != "" || !res.replaced || res.path != "/work/AGENTS.md" {
		t.Fatalf("result = %+v", res)
	}
	if fi.forceN != 1 {
		t.Fatalf("forceN = %d", fi.forceN)
	}

	next, _ := m.applyInitResult(res)
	nm := next.(Model)
	if !strings.Contains(nm.notice, "updated AGENTS.md") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestInitUnavailable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Init = nil
	next, cmd := m.handleCommand("/init")
	if cmd != nil {
		t.Fatal("unexpected cmd")
	}
	nm := next.(Model)
	if !strings.Contains(nm.notice, "unavailable") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestInitExistsError(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Init = &fakeInit{existEr: errors.New("stat failed")}
	next, _ := m.handleCommand("/init")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "stat failed") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestWelcomeMentionsInitWhenMissing(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m.services.Init = &fakeInit{exists: false}
	m.providerName = ""
	plain := ansi.Strip(m.welcomeView(100, 40))
	if !strings.Contains(plain, "/init") {
		t.Fatalf("welcome missing /init CTA:\n%s", plain)
	}
}

func TestWelcomeFirstRunMentionsInit(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m.firstRun = true
	m.providerName = ""
	plain := ansi.Strip(m.welcomeView(100, 40))
	if !strings.Contains(plain, "/init") && !strings.Contains(plain, "AGENTS.md") {
		t.Fatalf("first-run missing init mention:\n%s", plain)
	}
}
