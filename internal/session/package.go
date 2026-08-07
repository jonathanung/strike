package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/secret"
)

const (
	// PackageFormat identifies a portable session support/export bundle.
	PackageFormat = "strike.session"
	// PackageVersion is the portable package document version (not log schema).
	PackageVersion = 1
)

// Package is a versioned, secret-redacted portable snapshot of one session
// (metadata + ordered event envelopes). Used for support bundles and
// export/import round-trips. Distinct from markdown /export (#221) and from
// durable checkpoint stacks under ~/.strike/checkpoints/ (#573).
type Package struct {
	Format        string              `json:"format"`
	Version       int                 `json:"version"`
	SchemaVersion int                 `json:"schemaVersion"`
	ExportedAt    time.Time           `json:"exportedAt"`
	SourceID      string              `json:"sourceId,omitempty"`
	Meta          Meta                `json:"meta"`
	Redacted      bool                `json:"redacted"`
	Events        []protocol.Envelope `json:"events"`
}

// ExportPackage builds a redacted portable package for session id under dir.
// Events are re-scrubbed via secret.RedactEvent; envelope times are preserved.
func ExportPackage(dir, id string) (Package, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Package{}, err
	}
	path := LogPath(dir, id)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Package{}, fmt.Errorf("session %q not found", id)
		}
		return Package{}, err
	}
	if _, err := InspectSchemaVersion(path); err != nil {
		return Package{}, err
	}
	meta, err := ReadMeta(dir, id)
	if err != nil {
		return Package{}, err
	}
	// Scrub free-text meta fields that may have been set before redaction.
	meta.Title = scrubMetaString(meta.Title)
	meta.PRURL = scrubMetaString(meta.PRURL)

	timed, err := ReplayTimed(path)
	if err != nil {
		return Package{}, err
	}
	events := make([]protocol.Envelope, 0, len(timed))
	for _, te := range timed {
		redacted := secret.RedactEvent(te.Event)
		env, err := protocol.Wrap(redacted)
		if err != nil {
			return Package{}, fmt.Errorf("export event: %w", err)
		}
		// Preserve durable log time for sequence fidelity.
		if !te.Time.IsZero() {
			env.Time = te.Time.UTC()
		}
		events = append(events, env)
	}
	return Package{
		Format:        PackageFormat,
		Version:       PackageVersion,
		SchemaVersion: LogSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		SourceID:      id,
		Meta:          meta,
		Redacted:      true,
		Events:        events,
	}, nil
}

// WritePackage writes pkg as pretty JSON to path (atomic replace).
func WritePackage(path string, pkg Package) error {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return fmt.Errorf("export path is empty")
	}
	if pkg.Format == "" {
		pkg.Format = PackageFormat
	}
	if pkg.Version == 0 {
		pkg.Version = PackageVersion
	}
	if pkg.SchemaVersion == 0 {
		pkg.SchemaVersion = LogSchemaVersion
	}
	if pkg.ExportedAt.IsZero() {
		pkg.ExportedAt = time.Now().UTC()
	}
	pkg.Redacted = true
	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data)
}

// ReadPackage loads a portable session package from path.
func ReadPackage(path string) (Package, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Package{}, fmt.Errorf("read session package: %w", err)
	}
	return DecodePackage(data)
}

// DecodePackage parses and validates a portable session package document.
func DecodePackage(data []byte) (Package, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return Package{}, fmt.Errorf("session package: empty file")
	}
	var pkg Package
	if err := json.Unmarshal(data, &pkg); err != nil {
		return Package{}, fmt.Errorf("session package: bad JSON: %w", err)
	}
	if pkg.Format != PackageFormat {
		return Package{}, fmt.Errorf("session package: unsupported format %q", pkg.Format)
	}
	if pkg.Version != PackageVersion {
		return Package{}, fmt.Errorf("session package: unsupported version %d (want %d)", pkg.Version, PackageVersion)
	}
	if pkg.SchemaVersion > LogSchemaVersion {
		return Package{}, &SchemaVersionError{Found: pkg.SchemaVersion, Support: LogSchemaVersion}
	}
	if pkg.SchemaVersion < 0 {
		return Package{}, fmt.Errorf("session package: invalid schemaVersion %d", pkg.SchemaVersion)
	}
	return pkg, nil
}

// ImportPackage creates a new session from a portable package under dir.
// The new session gets a fresh id; meta.ForkedFrom is left empty (import is
// not a live fork). Source lineage is available on the package's SourceID.
// Returns the new session Info (closed after write).
func ImportPackage(dir string, pkg Package) (Info, error) {
	return importPackageInto(NewManager(dir), pkg)
}

func importPackageInto(m *Manager, pkg Package) (Info, error) {
	if m == nil {
		return Info{}, fmt.Errorf("session package: nil manager")
	}
	if pkg.Format == "" && pkg.Version == 0 {
		return Info{}, fmt.Errorf("session package: empty")
	}
	if pkg.Format != PackageFormat {
		return Info{}, fmt.Errorf("session package: unsupported format %q", pkg.Format)
	}
	if pkg.Version != PackageVersion {
		return Info{}, fmt.Errorf("session package: unsupported version %d", pkg.Version)
	}
	if pkg.SchemaVersion > LogSchemaVersion {
		return Info{}, &SchemaVersionError{Found: pkg.SchemaVersion, Support: LogSchemaVersion}
	}

	title := strings.TrimSpace(pkg.Meta.Title)
	if title == "" {
		title = "imported session"
	}
	info, err := m.Create(CreateOptions{
		Title:      title,
		ProjectKey: strings.TrimSpace(pkg.Meta.ProjectKey),
	})
	if err != nil {
		return Info{}, fmt.Errorf("import: create: %w", err)
	}
	// Restore portable meta fields (not live worktree/PR side-effects by default).
	if _, err := UpdateMeta(m.dir, info.ID, func(meta *Meta) {
		meta.Title = title
		meta.ProjectKey = strings.TrimSpace(pkg.Meta.ProjectKey)
		// Do not copy ParentSessionID / LeadSessionID / Worktree* — import is a
		// new root. ForkedFrom stays empty (distinct from Manager.Fork).
		if pkg.Meta.CreatedAt != "" {
			meta.CreatedAt = pkg.Meta.CreatedAt
		}
	}); err != nil {
		_ = m.Destroy(info.ID)
		return Info{}, fmt.Errorf("import: meta: %w", err)
	}

	for i, env := range pkg.Events {
		if err := checkEnvelopeVersion(env); err != nil {
			_ = m.Destroy(info.ID)
			return Info{}, fmt.Errorf("import event %d: %w", i, err)
		}
		ev, err := env.Decode()
		if err != nil {
			_ = m.Destroy(info.ID)
			return Info{}, fmt.Errorf("import event %d: %w", i, err)
		}
		if err := m.Append(info.ID, secret.RedactEvent(ev)); err != nil {
			_ = m.Destroy(info.ID)
			return Info{}, fmt.Errorf("import event %d: %w", i, err)
		}
	}
	if err := m.Close(info.ID); err != nil {
		return Info{}, err
	}
	return m.Get(info.ID)
}

func scrubMetaString(s string) string {
	return secret.Redact(s)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".strike-session-*.tmp")
	if err != nil {
		return fmt.Errorf("create session export temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write session export: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod session export: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync session export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close session export: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace session export: %w", err)
	}
	cleanup = false
	return nil
}
