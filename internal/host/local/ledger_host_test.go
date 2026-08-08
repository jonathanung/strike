package local

import (
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/ledger"
)

func TestLedgerNilStore(t *testing.T) {
	if NewLedger(nil) != nil {
		t.Fatal("nil store should yield nil host.Ledger")
	}
}

func TestLedgerActiveHistoryAndRedaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := ledger.Open(t.TempDir(), "proj-led")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewLedger(store)

	active, err := store.Append(ledger.AppendInput{
		Kind: ledger.KindDecision, Statement: "use postgres sk-ant-ledgersecret01",
		AuthorSession: "s1", AuthorRoot: "r1", Confidence: ledger.ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Append(ledger.AppendInput{
		Kind: ledger.KindAssumption, Statement: "old assumption",
		AuthorSession: "s1", AuthorRoot: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Invalidate(old.ID, ledger.InvalidateInput{Reason: "contradicted"}); err != nil {
		t.Fatal(err)
	}

	slice, err := svc.ActiveSlice("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(slice) != 1 || slice[0].ID != active.ID {
		t.Fatalf("active slice: %+v", slice)
	}
	if strings.Contains(slice[0].Statement, "sk-ant-ledgersecret01") {
		t.Fatalf("statement not redacted: %q", slice[0].Statement)
	}

	hist, err := svc.List(host.LedgerListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) < 2 {
		t.Fatalf("history should include invalidated: %d", len(hist))
	}
	var sawInvalid bool
	for _, e := range hist {
		if e.ID == old.ID && e.Status == ledger.StatusInvalidated {
			sawInvalid = true
		}
	}
	if !sawInvalid {
		t.Fatal("expected invalidated entry in history")
	}

	got, ok, err := svc.Get(old.ID)
	if err != nil || !ok {
		t.Fatalf("get invalidated: ok=%v err=%v", ok, err)
	}
	if got.Status != ledger.StatusInvalidated || got.InvalidateReason == "" {
		t.Fatalf("provenance missing: %+v", got)
	}

	// Missing capability pattern: empty list filter still works.
	page, err := svc.List(host.LedgerListFilter{Limit: 1, Offset: 0})
	if err != nil || len(page) != 1 {
		t.Fatalf("page: len=%d err=%v", len(page), err)
	}
	if _, ok, err := svc.Get("missing-id"); err != nil || ok {
		t.Fatalf("missing get: ok=%v err=%v", ok, err)
	}
}

func TestLedgerConcurrentReads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := ledger.Open(t.TempDir(), "proj-led-race")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewLedger(store)
	e, err := store.Append(ledger.AppendInput{
		Kind: ledger.KindConstraint, Statement: "no secrets in logs",
		AuthorSession: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.List(host.LedgerListFilter{}); err != nil {
				t.Errorf("list: %v", err)
			}
			if _, err := svc.ActiveSlice("", ""); err != nil {
				t.Errorf("active: %v", err)
			}
			if _, ok, err := svc.Get(e.ID); err != nil || !ok {
				t.Errorf("get: ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
}
