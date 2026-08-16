package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestBashGHPRRecordsSessionMeta(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'https://github.com/acme/repo/pull/123'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	sessionDir := t.TempDir()
	const sessionID = "sess-meta-1"
	var persisted protocol.SessionMeta
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       sessionID,
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         dir,
		SandboxMode:     "off", // CI may lack OS sandbox backend (#1030 fail-closed)
		Rules: []permission.Ruleset{{
			{Permission: "bash", Pattern: "*", Action: permission.Allow},
		}},
		PersistSessionMeta: func(m protocol.SessionMeta) error {
			persisted = m
			_, err := session.UpdateMeta(sessionDir, sessionID, func(meta *session.Meta) {
				meta.PRURL = m.PRURL
				meta.PRNumber = m.PRNumber
				meta.PRState = session.NormalizePRState(m.PRState)
				if meta.PRState == "" {
					meta.PRState = session.PRStateOpen
				}
			})
			return err
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run gh pr create --title t --body b"}

	var sawMeta protocol.SessionMeta
	var sawMetaOK bool
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session.meta")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.SessionMeta:
				sawMeta = ev
				sawMetaOK = true
			case protocol.TurnCompleted:
				if !sawMetaOK {
					t.Fatal("TurnCompleted before SessionMeta")
				}
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			case protocol.ToolCallEnd:
				if ev.IsError {
					t.Fatalf("tool failed: %s", ev.Output)
				}
			}
		}
	}
done:
	if sawMeta.PRURL != "https://github.com/acme/repo/pull/123" || sawMeta.PRNumber != 123 || sawMeta.PRState != "open" {
		t.Fatalf("SessionMeta event = %+v", sawMeta)
	}
	if persisted.PRURL != sawMeta.PRURL || persisted.PRNumber != sawMeta.PRNumber || persisted.PRState != sawMeta.PRState {
		t.Fatalf("persisted = %+v, event = %+v", persisted, sawMeta)
	}
	got, err := session.ReadMeta(sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != sawMeta.PRURL || got.PRNumber != 123 || got.PRState != session.PRStateOpen {
		t.Fatalf("sidecar = %+v", got)
	}
	if sawMeta.SessionID != sessionID {
		t.Errorf("correlation session = %q, want %q", sawMeta.SessionID, sessionID)
	}
}
