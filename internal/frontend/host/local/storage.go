package local

import (
	"errors"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/knowledge"
	"github.com/jonathanung/strike/internal/persist/memory"
)

// scopedMemory adds a global store beside the project memory adapter.
type scopedMemory struct {
	memoryAdapter
	global *memory.Store
}

// AdaptMemory wraps a project store as host.Memory so ScopeMemory can attach
// a global store beside it.
func AdaptMemory(store *memory.Store) host.Memory {
	if store == nil {
		return nil
	}
	return memoryAdapter{store: store}
}

// ScopeMemory returns a Memory that also serves scope=global. project may
// already be a memoryAdapter from New. A nil global keeps project-only behavior
// but still reports project file paths.
func ScopeMemory(project host.Memory, global *memory.Store) host.Memory {
	if project == nil && global == nil {
		return nil
	}
	base, ok := project.(memoryAdapter)
	if !ok {
		if global == nil {
			return project
		}
		return &scopedMemory{global: global}
	}
	return &scopedMemory{memoryAdapter: base, global: global}
}

func (m *scopedMemory) storeFor(scope string) (*memory.Store, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		if m.memoryAdapter.store == nil {
			return nil, errors.New("project memory is not configured")
		}
		return m.memoryAdapter.store, nil
	case "global":
		if m.global == nil {
			return nil, errors.New("global memory is not configured")
		}
		return m.global, nil
	default:
		return nil, errors.New("scope must be project or global")
	}
}

func (m *scopedMemory) ListScope(scope, tag string) ([]host.MemoryEntry, error) {
	store, err := m.storeFor(scope)
	if err != nil {
		return nil, err
	}
	entries, err := store.List(tag)
	if err != nil {
		return nil, err
	}
	out := make([]host.MemoryEntry, len(entries))
	for i, e := range entries {
		out[i] = memoryEntryFrom(e)
	}
	return out, nil
}

func (m *scopedMemory) PutScope(scope, key, value, author string, tags []string) (host.MemoryEntry, error) {
	store, err := m.storeFor(scope)
	if err != nil {
		return host.MemoryEntry{}, err
	}
	if err := store.PutAuthored(key, value, author, tags); err != nil {
		return host.MemoryEntry{}, err
	}
	got, ok, err := store.Get(memory.ScrubKey(key))
	if err != nil {
		return host.MemoryEntry{}, err
	}
	if !ok {
		return host.MemoryEntry{}, errors.New("memory entry missing after put")
	}
	return memoryEntryFrom(got), nil
}

func (m *scopedMemory) DeleteScope(scope, key string) error {
	store, err := m.storeFor(scope)
	if err != nil {
		return err
	}
	if err := store.Delete(key); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return store.Delete(memory.ScrubKey(key))
		}
		return err
	}
	return nil
}

func (m *scopedMemory) Files(scope string) (string, string) {
	store, err := m.storeFor(scope)
	if err != nil || store == nil {
		return "", ""
	}
	return store.MarkdownPath(), store.AuditPath()
}

// List/Get/Put/Delete on scopedMemory follow the embedded project adapter.
// When the project adapter store is nil, fall through is not available.

type knowledgeAdapter struct {
	project *knowledge.Store
	global  *knowledge.Store
}

// NewKnowledge wires project and global pin stores. Both may be nil.
func NewKnowledge(project, global *knowledge.Store) host.Knowledge {
	if project == nil && global == nil {
		return nil
	}
	return knowledgeAdapter{project: project, global: global}
}

func (k knowledgeAdapter) store(scope string) (*knowledge.Store, error) {
	switch strings.TrimSpace(scope) {
	case "", "project":
		if k.project == nil {
			return nil, errors.New("project knowledge is not configured")
		}
		return k.project, nil
	case "global":
		if k.global == nil {
			return nil, errors.New("global knowledge is not configured")
		}
		return k.global, nil
	default:
		return nil, errors.New("scope must be project or global")
	}
}

func (k knowledgeAdapter) List(scope string) ([]host.KnowledgePin, error) {
	store, err := k.store(scope)
	if err != nil {
		return nil, err
	}
	pins, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.KnowledgePin, len(pins))
	for i, p := range pins {
		out[i] = host.KnowledgePin{
			Path: p.Path, Note: p.Note, Author: p.Author, PinnedAt: p.PinnedAt, Scope: store.Scope(),
		}
	}
	return out, nil
}

func (k knowledgeAdapter) Put(scope, path, note, author string) (host.KnowledgePin, error) {
	store, err := k.store(scope)
	if err != nil {
		return host.KnowledgePin{}, err
	}
	pin, err := store.Put(path, note, author)
	if err != nil {
		return host.KnowledgePin{}, err
	}
	return host.KnowledgePin{
		Path: pin.Path, Note: pin.Note, Author: pin.Author, PinnedAt: pin.PinnedAt, Scope: store.Scope(),
	}, nil
}

func (k knowledgeAdapter) Delete(scope, path string) error {
	store, err := k.store(scope)
	if err != nil {
		return err
	}
	return store.Delete(path)
}

func (k knowledgeAdapter) Files(scope string) string {
	store, err := k.store(scope)
	if err != nil || store == nil {
		return ""
	}
	return store.MarkdownPath()
}
