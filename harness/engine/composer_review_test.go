package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestMaybeTitleSessionOnceUnderRace(t *testing.T) {
	eng := New(Options{})
	const n = 16
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(n)
	done.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			ready.Done()
			<-start
			eng.maybeTitleSession("race the title")
			done.Done()
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	got := 0
	for {
		select {
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.SessionTitled); ok {
				got++
			}
		default:
			if got != 1 {
				t.Fatalf("SessionTitled count = %d, want 1", got)
			}
			return
		}
	}
}

func TestComposerShellMarksTurnUncovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := New(Options{
		WorkDir:             dir,
		SandboxMode:         "off",
		SandboxAllowDegrade: true,
		Rules: []permission.Ruleset{{
			{Permission: "bash", Pattern: "*", Action: permission.Allow},
		}},
	})
	eng.checkpoints.BeginTurn("composer-shell")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	eng.execComposerShell(ctx, protocol.Correlation{}, "bang-test", "rm notes.txt", eng.bashSandboxPolicy(), nil)
	eng.checkpoints.CommitTurn()
	peek := eng.checkpoints.Peek()
	if len(peek.Uncovered) == 0 && !peek.HadFiles {
		t.Fatalf("shell mutation invisible to rewind: %+v", peek)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rm notes.txt did not remove the file: %v", err)
	}
}
