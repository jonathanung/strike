package local

import (
	"errors"
	"fmt"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/persist/artifact"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

const (
	defaultArtifactListLimit = 100
	maxArtifactListLimit     = 200
)

// NewArtifacts adapts *artifact.Store to host.Artifacts. A nil store yields nil.
func NewArtifacts(store *artifact.Store) host.Artifacts {
	if store == nil {
		return nil
	}
	return artifactsAdapter{store: store}
}

type artifactsAdapter struct {
	store *artifact.Store
}

func (a artifactsAdapter) List(actorSession, actorRoot string, filter host.ArtifactListFilter) ([]host.ArtifactMeta, error) {
	limit, offset := boundPage(filter.Limit, filter.Offset, defaultArtifactListLimit, maxArtifactListLimit)
	list, err := a.store.List(actorSession, actorRoot, artifact.ListFilter{
		Type:           filter.Type,
		Scope:          filter.Scope,
		SessionID:      filter.SessionID,
		IncludeExpired: filter.IncludeExpired,
	})
	if err != nil {
		return nil, err
	}
	if offset > len(list) {
		return []host.ArtifactMeta{}, nil
	}
	list = list[offset:]
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]host.ArtifactMeta, len(list))
	for i, m := range list {
		out[i] = toHostArtifactMeta(m)
	}
	return out, nil
}

func (a artifactsAdapter) Get(id, actorSession, actorRoot string) (host.Artifact, bool, error) {
	art, ok, err := a.store.Get(id, actorSession, actorRoot)
	if errors.Is(err, artifact.ErrDenied) {
		// Browser surface: denied looks like missing (no existence leak).
		return host.Artifact{}, false, nil
	}
	if err != nil || !ok {
		return host.Artifact{}, ok, err
	}
	return toHostArtifact(art), true, nil
}

func (a artifactsAdapter) GetVersion(id string, version int, actorSession, actorRoot string) (host.Artifact, bool, error) {
	if version < 1 {
		return host.Artifact{}, false, fmt.Errorf("artifact version must be >= 1")
	}
	art, ok, err := a.store.GetVersion(id, version, actorSession, actorRoot)
	if errors.Is(err, artifact.ErrDenied) {
		return host.Artifact{}, false, nil
	}
	if err != nil || !ok {
		return host.Artifact{}, ok, err
	}
	return toHostArtifact(art), true, nil
}

func toHostArtifactMeta(m artifact.Meta) host.ArtifactMeta {
	out := host.ArtifactMeta{
		ID:           m.ID,
		Type:         m.Type,
		Title:        redact.String(m.Title),
		Version:      m.Version,
		Scope:        m.Scope,
		SessionID:    m.SessionID,
		Access:       m.Access,
		OwnerSession: m.OwnerSession,
		OwnerRoot:    m.OwnerRoot,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
	if m.ExpiresAt != nil {
		t := *m.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

func toHostArtifact(a artifact.Artifact) host.Artifact {
	out := host.Artifact{
		ID:           a.ID,
		Type:         a.Type,
		Title:        redact.String(a.Title),
		Content:      redact.String(a.Content),
		Version:      a.Version,
		Scope:        a.Scope,
		SessionID:    a.SessionID,
		Access:       a.Access,
		OwnerSession: a.OwnerSession,
		OwnerRoot:    a.OwnerRoot,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
	if a.ExpiresAt != nil {
		t := *a.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

func boundPage(limit, offset, def, max int) (int, int) {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
