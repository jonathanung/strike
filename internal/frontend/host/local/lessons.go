package local

import (
	"errors"
	"fmt"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/memory"
)

// Lessons loop (#1376): pending proposals live with the project store;
// approval writes a lesson-tagged entry into the proposal's scope.

var _ host.MemoryLessons = (*scopedMemory)(nil)

// approvedAuthor is recorded on entries created by approving a proposal.
const approvedAuthor = "operator (approved proposal)"

func (m *scopedMemory) proposalStore() (*memory.Store, error) {
	if m.memoryAdapter.store == nil {
		return nil, errors.New("project memory is not configured")
	}
	return m.memoryAdapter.store, nil
}

func proposalFrom(p memory.Proposal) host.MemoryProposal {
	return host.MemoryProposal{
		ID:        p.ID,
		Key:       p.Key,
		Value:     p.Value,
		Scope:     p.Scope,
		Session:   p.Session,
		CreatedAt: p.CreatedAt,
	}
}

func (m *scopedMemory) Proposals() ([]host.MemoryProposal, error) {
	store, err := m.proposalStore()
	if err != nil {
		return nil, err
	}
	list, err := store.Proposals()
	if err != nil {
		return nil, err
	}
	out := make([]host.MemoryProposal, len(list))
	for i, p := range list {
		out[i] = proposalFrom(p)
	}
	return out, nil
}

func (m *scopedMemory) Propose(p host.MemoryProposal) (host.MemoryProposal, error) {
	store, err := m.proposalStore()
	if err != nil {
		return host.MemoryProposal{}, err
	}
	scope, err := memory.NormalizeScope(p.Scope)
	if err != nil {
		return host.MemoryProposal{}, err
	}
	// Refuse a scope this host cannot approve into, up front.
	if _, err := m.storeFor(scope); err != nil {
		return host.MemoryProposal{}, err
	}
	got, err := store.Propose(memory.Proposal{Key: p.Key, Value: p.Value, Scope: scope, Session: p.Session})
	if err != nil {
		return host.MemoryProposal{}, err
	}
	return proposalFrom(got), nil
}

func (m *scopedMemory) EditProposal(id, key, value, scope string) (host.MemoryProposal, error) {
	store, err := m.proposalStore()
	if err != nil {
		return host.MemoryProposal{}, err
	}
	norm, err := memory.NormalizeScope(scope)
	if err != nil {
		return host.MemoryProposal{}, err
	}
	if _, err := m.storeFor(norm); err != nil {
		return host.MemoryProposal{}, err
	}
	got, err := store.EditProposal(id, key, value, norm)
	if err != nil {
		return host.MemoryProposal{}, err
	}
	return proposalFrom(got), nil
}

func (m *scopedMemory) ApproveProposal(id string, replace bool) (host.MemoryEntry, error) {
	store, err := m.proposalStore()
	if err != nil {
		return host.MemoryEntry{}, err
	}
	// Take first so two concurrent approvals cannot both store it; restore
	// on any failure so the proposal is never lost.
	p, err := store.TakeProposal(id)
	if err != nil {
		return host.MemoryEntry{}, err
	}
	entry, err := m.storeApproved(p, replace)
	if err != nil {
		if rerr := store.RestoreProposal(p); rerr != nil {
			return host.MemoryEntry{}, fmt.Errorf("%w (and restoring the proposal failed: %v)", err, rerr)
		}
		return host.MemoryEntry{}, err
	}
	return entry, nil
}

func (m *scopedMemory) storeApproved(p memory.Proposal, replace bool) (host.MemoryEntry, error) {
	target, err := m.storeFor(p.Scope)
	if err != nil {
		return host.MemoryEntry{}, err
	}
	if !replace {
		if _, exists, err := target.Get(p.Key); err != nil {
			return host.MemoryEntry{}, err
		} else if exists {
			return host.MemoryEntry{}, host.ErrMemoryKeyExists
		}
	}
	if err := target.PutAuthored(p.Key, p.Value, approvedAuthor, []string{memory.TagLesson}); err != nil {
		return host.MemoryEntry{}, err
	}
	got, ok, err := target.Get(p.Key)
	if err != nil {
		return host.MemoryEntry{}, err
	}
	if !ok {
		return host.MemoryEntry{}, errors.New("memory entry missing after approve")
	}
	return memoryEntryFrom(got), nil
}

func (m *scopedMemory) DismissProposal(id string) error {
	store, err := m.proposalStore()
	if err != nil {
		return err
	}
	_, err = store.TakeProposal(id)
	return err
}

func (m *scopedMemory) Injected(sessionID string) ([]host.MemoryInjection, error) {
	store := m.memoryAdapter.store
	if store == nil {
		store = m.global
	}
	if store == nil {
		return []host.MemoryInjection{}, nil
	}
	list, err := store.Injected(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]host.MemoryInjection, len(list))
	for i, e := range list {
		out[i] = host.MemoryInjection{
			Scope:      e.Scope,
			Key:        e.Key,
			Value:      e.Value,
			Tags:       append([]string(nil), e.Tags...),
			InjectedAt: e.InjectedAt,
		}
	}
	return out, nil
}
