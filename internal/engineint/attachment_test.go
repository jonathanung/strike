package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/provider/echo"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/attachment"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestUserInputImagesPersistAsRefs(t *testing.T) {
	store, err := attachment.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prov := newScriptedProvider(completedStep("saw it"))
	eng := engine.New(engine.Options{
		BuildDiagnostic:     enginebind.Diagnostic(),
		Select:              func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider:     "scripted",
		Registry:            tool.NewRegistry(),
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
		Attachments:         enginebind.Attachments(store),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	img := protocol.ImageAttachment{MIME: "image/png", Data: "iVBORw0KGgo="}
	eng.Ops() <- protocol.UserInput{Text: "look", Images: []protocol.ImageAttachment{img}}

	var um protocol.UserMessage
	deadline := time.After(2 * time.Second)
	for um.Text == "" {
		select {
		case ev := <-eng.Events():
			if got, ok := ev.(protocol.UserMessage); ok {
				um = got
			}
		case <-deadline:
			t.Fatal("timeout waiting for UserMessage")
		}
	}
	if um.Text != "look" || len(um.Images) != 1 {
		t.Fatalf("UserMessage = %#v", um)
	}
	if um.Images[0].Data != "" {
		t.Fatalf("history embedded payload: %#v", um.Images[0])
	}
	if um.Images[0].Ref == "" || um.Images[0].SHA256 == "" || um.Images[0].MIME != "image/png" {
		t.Fatalf("missing ref metadata: %#v", um.Images[0])
	}
	if _, _, err := store.Get(um.Images[0].Ref); err != nil {
		t.Fatalf("store get: %v", err)
	}

	req := receiveRequest(t, prov.requests)
	last := req.Messages[len(req.Messages)-1]
	if len(last.Images) != 1 || last.Images[0].MIME != "image/png" || len(last.Images[0].Data) == 0 {
		t.Fatalf("provider images = %#v", last.Images)
	}
	_ = waitForTurnCompleted(t, eng.Events())
}

func TestRestoreHydratesAttachmentRefs(t *testing.T) {
	store, err := attachment.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	meta, err := store.Put(raw, attachment.PutInput{MIME: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	ref := attachment.RefFor(meta.SHA256)
	restored := engine.Restore([]protocol.Event{
		protocol.UserMessage{Text: "look", Images: []protocol.ImageAttachment{{
			MIME: "image/png", Ref: ref, SHA256: meta.SHA256, Bytes: meta.Bytes, Kind: protocol.AttachmentKindImage,
		}}},
	})
	if len(restored.Messages) != 1 || len(restored.Messages[0].Images) != 1 {
		t.Fatalf("restored = %#v", restored.Messages)
	}
	if restored.Messages[0].Images[0].Ref != ref {
		t.Fatalf("ref = %#v", restored.Messages[0].Images[0])
	}
	prov := newScriptedProvider(completedStep("ok"))
	eng := engine.New(engine.Options{
		BuildDiagnostic:     enginebind.Diagnostic(),
		Select:              func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider:     "scripted",
		Registry:            tool.NewRegistry(),
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
		Attachments:         enginebind.Attachments(store),
		InitialMessages:     restored.Messages,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "again"}
	req := receiveRequest(t, prov.requests)
	found := false
	for _, msg := range req.Messages {
		if msg.Role == provider.RoleUser && len(msg.Images) == 1 && len(msg.Images[0].Data) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("hydrated images missing: %#v", req.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())
}

func TestEchoDropsImagesByCapability(t *testing.T) {
	store, err := attachment.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	eng := engine.New(engine.Options{
		BuildDiagnostic:     enginebind.Diagnostic(),
		Select:              func(string) (provider.Provider, string, error) { return echo.New(), "echo", nil },
		InitialProvider:     "echo",
		Registry:            tool.NewRegistry(),
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
		Attachments:         enginebind.Attachments(store),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{
		Text:   "look",
		Images: []protocol.ImageAttachment{{MIME: "image/png", Data: "iVBORw0KGgo="}},
	}
	var sawVisible bool
	deadline := time.After(2 * time.Second)
	for !sawVisible {
		select {
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok && strings.Contains(err.Message, "does not support") {
				sawVisible = true
			}
			if _, ok := ev.(protocol.TurnCompleted); ok && !sawVisible {
				t.Fatal("turn completed without visible capability error")
			}
		case <-deadline:
			t.Fatal("timeout waiting for capability error")
		}
	}
}
