package local

import (
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/persist/ledger"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

const (
	defaultLedgerListLimit = 100
	maxLedgerListLimit     = 200
)

// NewLedger adapts *ledger.Store to host.Ledger. A nil store yields nil.
func NewLedger(store *ledger.Store) host.Ledger {
	if store == nil {
		return nil
	}
	return ledgerAdapter{store: store}
}

type ledgerAdapter struct {
	store *ledger.Store
}

func (a ledgerAdapter) ActiveSlice(path, taskID string) ([]host.LedgerEntry, error) {
	list, err := a.store.ActiveSlice(path, taskID)
	if err != nil {
		return nil, err
	}
	return toHostLedgerEntries(list), nil
}

func (a ledgerAdapter) List(filter host.LedgerListFilter) ([]host.LedgerEntry, error) {
	limit, offset := boundPage(filter.Limit, filter.Offset, defaultLedgerListLimit, maxLedgerListLimit)
	list, err := a.store.List(ledger.ListFilter{
		Status:        filter.Status,
		Kind:          filter.Kind,
		Path:          filter.Path,
		TaskID:        filter.TaskID,
		AuthorSession: filter.AuthorSession,
	})
	if err != nil {
		return nil, err
	}
	if offset > len(list) {
		return []host.LedgerEntry{}, nil
	}
	list = list[offset:]
	if len(list) > limit {
		list = list[:limit]
	}
	return toHostLedgerEntries(list), nil
}

func (a ledgerAdapter) Get(id string) (host.LedgerEntry, bool, error) {
	e, ok, err := a.store.Get(id)
	if err != nil || !ok {
		return host.LedgerEntry{}, ok, err
	}
	return toHostLedgerEntry(e), true, nil
}

func toHostLedgerEntries(list []ledger.Entry) []host.LedgerEntry {
	out := make([]host.LedgerEntry, len(list))
	for i, e := range list {
		out[i] = toHostLedgerEntry(e)
	}
	return out
}

func redactStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact.String(s)
	}
	return out
}

func toHostLedgerEntry(e ledger.Entry) host.LedgerEntry {
	out := host.LedgerEntry{
		ID:                 e.ID,
		Kind:               e.Kind,
		Statement:          redact.String(e.Statement),
		Confidence:         e.Confidence,
		EvidenceRefs:       redactStringSlice(e.EvidenceRefs),
		Status:             e.Status,
		ScopePaths:         append([]string(nil), e.ScopePaths...),
		ScopeTaskIDs:       append([]string(nil), e.ScopeTaskIDs...),
		AuthorSession:      e.AuthorSession,
		AuthorAgent:        e.AuthorAgent,
		AuthorRoot:         e.AuthorRoot,
		CreatedAt:          e.CreatedAt,
		UpdatedAt:          e.UpdatedAt,
		InvalidateReason:   redact.String(e.InvalidateReason),
		InvalidateEvidence: redactStringSlice(e.InvalidateEvidence),
		SupersededBy:       e.SupersededBy,
		Supersedes:         e.Supersedes,
	}
	if e.InvalidatedAt != nil {
		t := *e.InvalidatedAt
		out.InvalidatedAt = &t
	}
	return out
}
