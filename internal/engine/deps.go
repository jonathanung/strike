package engine

import (
	"context"
	"errors"
	"strings"
)

// Kernel seams for Strike product stores. Implementations live outside
// engine (cmd/strike via internal/enginebind) so a future harness module
// cannot import internal product packages.
//
// String scrubbing uses pkg/redact. Secret-ref resolution stays in tools via
// Options.BashSecrets plus AuditRecorder.RecordSecretRefUse.

// MemorySource auto-loads tagged project memory into the system prompt.
type MemorySource interface {
	AutoLoad() (text string, omitted int, err error)
}

// LedgerEntry is the kernel projection of one active ledger row.
type LedgerEntry struct {
	ID            string
	Kind          string
	Status        string
	Statement     string
	Confidence    string
	EvidenceRefs  []string
	ScopePaths    []string
	ScopeTaskIDs  []string
	AuthorSession string
	Reason        string
	Supersedes    string
	SupersededBy  string
	// Freshness is "stale" when pinned evidence no longer matches; otherwise
	// empty/"fresh". Product adapters assess this before returning the row.
	Freshness string
}

// Ledger kinds/status used by residue extraction (wire values).
const (
	LedgerKindDecision   = "decision"
	LedgerKindAssumption = "assumption"
	LedgerKindConstraint = "constraint"
	LedgerStatusActive   = "active"
	LedgerConfidenceMed  = "medium"
)

// LedgerSource auto-loads the active decision ledger and supplies residue rows.
type LedgerSource interface {
	AutoLoad(workDir string) (text string, omitted int, err error)
	ActiveSlice(path, taskID, workDir string) ([]LedgerEntry, error)
}

// PlanView is the kernel projection of a structured plan for handoff/prompt.
type PlanView struct {
	ID        string
	OwnerRoot string
	Status    string
	Title     string
	Version   int
	Sections  []PlanSectionView
}

// PlanSectionView is one plan section body shown to the implementer.
type PlanSectionView struct {
	ID    string
	Title string
	Body  string
}

// DelegateOutcome is the terminal result of a plan-section child.
type DelegateOutcome struct {
	Status string
	Title  *string
	Body   *string
	Detail string
}

// Plan lifecycle and delegate statuses (wire values; match persist stores).
const (
	PlanStatusDraft    = "draft"
	PlanStatusApproved = "approved"
	PlanStatusClosed   = "closed"

	DelegateApplied   = "applied"
	DelegateFailed    = "failed"
	DelegateCanceled  = "canceled"
	DelegateConflict  = "conflict"
	DelegateMalformed = "malformed"
	DelegateInFlight  = "in_flight"
)

// Plan ownership/CAS errors. Adapters must wrap product sentinels onto these.
var (
	ErrPlanNotOwner = errors.New("plan: only the owning root may mutate this plan")
	ErrPlanConflict = errors.New("plan: version conflict")
)

// PlanStore is the engine-facing surface for plan handoff and section-delegate
// completion. Product *plan.Store is adapted in enginebind.
type PlanStore interface {
	Get(id string) (PlanView, bool, error)
	SetStatus(id, actorRoot, status string, expectedVersion int) (PlanView, error)
	FinishSectionDelegate(id, actorRoot, sectionID, childID string, outcome DelegateOutcome) (PlanView, error)
}

// ArtifactNotice is the kernel projection of a typed artifact mutation.
type ArtifactNotice struct {
	ID        string
	Type      string
	Version   int
	Scope     string
	Title     string
	SessionID string
}

// LedgerNotice is the kernel projection of a ledger mutation event.
type LedgerNotice struct {
	ID            string
	Kind          string
	Status        string
	Statement     string
	Reason        string
	Supersedes    string
	SupersededBy  string
	AuthorSession string
}

// ArtifactProjector maps a product tool payload to an ArtifactNotice.
type ArtifactProjector func(payload any) (ArtifactNotice, bool)

// LedgerProjector maps a product tool payload to a LedgerNotice.
type LedgerProjector func(payload any) (LedgerNotice, bool)

// AttachmentPut is optional classification at ingest.
type AttachmentPut struct {
	MIME      string
	Name      string
	Kind      string
	SessionID string
}

// AttachmentMeta is stored-blob metadata returned after persist.
type AttachmentMeta struct {
	SHA256 string
	MIME   string
	Kind   string
	Name   string
	Bytes  int64
}

// AttachmentStore persists inbound user attachments by content hash.
type AttachmentStore interface {
	Put(raw []byte, in AttachmentPut) (AttachmentMeta, error)
	Get(ref string) (raw []byte, meta AttachmentMeta, err error)
}

// ErrEmptyAttachment is returned when decoded attachment bytes are empty.
var ErrEmptyAttachment = errors.New("attachment: empty payload")

// Worktree is one strike-managed git worktree bound to a child session.
type Worktree struct {
	Path     string
	Branch   string
	RepoRoot string
}

// ErrNotGitRepository is returned when worktree bind needs a git repo.
// Adapters must wrap product sentinels onto this value.
var ErrNotGitRepository = errors.New("not a git repository")

// WorktreeBinder creates, diffs, and removes per-child git worktrees.
type WorktreeBinder interface {
	Add(ctx context.Context, base, childID string) (Worktree, error)
	HeadRev(ctx context.Context, path string) string
	DiffUnified(ctx context.Context, path string) (string, error)
	Remove(ctx context.Context, repo, path, branch string) error
}

// CanonicalProviderID normalizes a provider id: trim, lowercase, and map the
// shipped gemini alias onto google. Empty input stays empty.
func CanonicalProviderID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "gemini" {
		return "google"
	}
	return id
}

// Child isolation modes (session.childIsolation / task.isolation).
const (
	ChildIsolationOff      = "off"
	ChildIsolationShared   = "shared"
	ChildIsolationWorktree = "worktree"
)

// NormalizeChildIsolation maps config/task strings to off|shared|worktree.
// Empty and unknown values become off (shared workdir).
func NormalizeChildIsolation(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ChildIsolationWorktree:
		return ChildIsolationWorktree
	case ChildIsolationShared, ChildIsolationOff:
		return ChildIsolationShared
	default:
		return ChildIsolationOff
	}
}

// WantChildWorktree reports whether a child spawn should get an isolated worktree.
func WantChildWorktree(sessionDefault, spawnOverride string) bool {
	mode := NormalizeChildIsolation(spawnOverride)
	if strings.TrimSpace(spawnOverride) == "" {
		mode = NormalizeChildIsolation(sessionDefault)
	}
	return mode == ChildIsolationWorktree
}
