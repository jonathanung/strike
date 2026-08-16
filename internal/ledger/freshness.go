package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Evidence pin kinds.
const (
	PinKindPath    = "path"
	PinKindSymbol  = "symbol"
	PinKindCommand = "command"
)

// Freshness states for an entry at read/inject time.
const (
	FreshValidated     = "validated"
	FreshStale         = "stale"
	FreshUnpinned      = "unpinned"
	FreshNotApplicable = "not_applicable"
)

const (
	maxPinPathLen    = 512
	maxPinHashLen    = 80
	maxPinSymbolLen  = 256
	maxPinCommandLen = 512
)

var (
	errEmptyPinPath    = errors.New("ledger: evidence pin path is required")
	errEmptyPinSymbol  = errors.New("ledger: evidence pin symbol is required")
	errEmptyPinCommand = errors.New("ledger: evidence pin command is required")
	errInvalidPinKind  = errors.New("ledger: evidence pin kind must be path, symbol, or command")
	errPinPathEscape   = errors.New("ledger: evidence pin path escapes workdir")
)

// EvidencePin is optional repository evidence that justified an assumption.
// Legacy entries omit this field; JSON v1 stays compatible.
type EvidencePin struct {
	Kind    string `json:"kind"`              // path | symbol | command
	Path    string `json:"path,omitempty"`    // workspace-relative or under workdir
	Hash    string `json:"hash,omitempty"`    // sha256:<hex> of file bytes
	Symbol  string `json:"symbol,omitempty"`  // identifier that must still occur in Path
	Command string `json:"command,omitempty"` // recorded verified command (not auto-rerun)
}

// Freshness is a computed overlay; it is not persisted.
type Freshness struct {
	State           string   // validated | stale | unpinned | not_applicable
	Reason          string   // human-readable when stale
	ChangedEvidence []string // pin descriptors that failed
}

// AssessFreshness reports whether e is still justified by pinned repo evidence.
// Decisions and constraints are never auto-staled. Command pins are stored but
// not re-executed. workDir empty skips IO and never marks stale.
func AssessFreshness(e Entry, workDir string) Freshness {
	if e.Kind != KindAssumption {
		return Freshness{State: FreshNotApplicable}
	}
	if e.Status != StatusActive {
		return Freshness{State: FreshNotApplicable}
	}
	pins := checkablePins(e.EvidencePins)
	if len(pins) == 0 {
		return Freshness{State: FreshUnpinned}
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		// Cannot observe the repo; do not false-stale.
		return Freshness{State: FreshValidated}
	}
	var changed []string
	var missing, mismatch bool
	for _, pin := range pins {
		hit, miss, desc := checkPin(pin, workDir)
		if hit {
			continue
		}
		changed = append(changed, desc)
		if miss {
			missing = true
		} else {
			mismatch = true
		}
	}
	if len(changed) == 0 {
		return Freshness{State: FreshValidated}
	}
	reason := "pinned evidence changed"
	if missing && !mismatch {
		reason = "pinned evidence missing"
	} else if missing && mismatch {
		reason = "pinned evidence changed or missing"
	}
	return Freshness{State: FreshStale, Reason: reason, ChangedEvidence: changed}
}

// PinDigest returns sha256:<hex> for data.
func PinDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SnapshotPathPin reads path under workDir and returns a path pin with current hash.
func SnapshotPathPin(workDir, rel string) (EvidencePin, error) {
	abs, err := resolvePinPath(workDir, rel)
	if err != nil {
		return EvidencePin{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return EvidencePin{}, err
	}
	return EvidencePin{Kind: PinKindPath, Path: strings.TrimSpace(rel), Hash: PinDigest(data)}, nil
}

func checkablePins(pins []EvidencePin) []EvidencePin {
	out := make([]EvidencePin, 0, len(pins))
	for _, p := range pins {
		switch p.Kind {
		case PinKindPath, PinKindSymbol:
			if strings.TrimSpace(p.Path) != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func checkPin(pin EvidencePin, workDir string) (ok, missing bool, desc string) {
	label := pinLabel(pin)
	abs, err := resolvePinPath(workDir, pin.Path)
	if err != nil {
		if errors.Is(err, errPinPathEscape) {
			return false, false, label + " escapes workdir"
		}
		return false, true, label + " missing"
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, true, label + " missing"
		}
		return false, true, label + " unreadable"
	}
	want := normalizeHash(pin.Hash)
	if want != "" {
		got := PinDigest(data)
		if !hashesEqual(want, got) {
			return false, false, fmt.Sprintf("%s hash-mismatch have=%s want=%s", label, got, want)
		}
	}
	if pin.Kind == PinKindSymbol {
		sym := strings.TrimSpace(pin.Symbol)
		if sym == "" || !bytesContainIdent(data, sym) {
			return false, true, label + " symbol missing"
		}
	}
	return true, false, ""
}

func pinLabel(p EvidencePin) string {
	path := strings.TrimSpace(p.Path)
	if p.Kind == PinKindSymbol {
		return "symbol:" + strings.TrimSpace(p.Symbol) + " in " + path
	}
	return "path:" + path
}

func normalizeHash(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if h == "" {
		return ""
	}
	if !strings.HasPrefix(h, "sha256:") {
		return "sha256:" + h
	}
	return h
}

func hashesEqual(a, b string) bool {
	return normalizeHash(a) == normalizeHash(b)
}

func bytesContainIdent(data []byte, ident string) bool {
	return ident != "" && strings.Contains(string(data), ident)
}

func resolvePinPath(workDir, rel string) (string, error) {
	workDir = strings.TrimSpace(workDir)
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errEmptyPinPath
	}
	if workDir == "" {
		return "", errEmptyPinPath
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	root = filepath.Clean(root)
	var candidate string
	if filepath.IsAbs(rel) {
		candidate = filepath.Clean(rel)
	} else {
		candidate = filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	}
	if !pathUnder(root, candidate) {
		return "", errPinPathEscape
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		if !pathUnder(root, resolved) {
			return "", errPinPathEscape
		}
		return resolved, nil
	}
	// Missing leaf: still confine the cleaned path.
	if !pathUnder(root, candidate) {
		return "", errPinPathEscape
	}
	return candidate, nil
}

func pathUnder(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, root+sep)
}

func clonePins(in []EvidencePin) []EvidencePin {
	if len(in) == 0 {
		return nil
	}
	return append([]EvidencePin(nil), in...)
}

func normalizePins(pins []EvidencePin) ([]EvidencePin, error) {
	if len(pins) == 0 {
		return nil, nil
	}
	if len(pins) > maxEvidence {
		return nil, fmt.Errorf("ledger: at most %d evidence_pins", maxEvidence)
	}
	seen := make(map[string]struct{}, len(pins))
	out := make([]EvidencePin, 0, len(pins))
	for i, p := range pins {
		kind := strings.ToLower(strings.TrimSpace(p.Kind))
		if kind == "" {
			if strings.TrimSpace(p.Path) != "" && strings.TrimSpace(p.Symbol) != "" {
				kind = PinKindSymbol
			} else if strings.TrimSpace(p.Command) != "" {
				kind = PinKindCommand
			} else {
				kind = PinKindPath
			}
		}
		switch kind {
		case PinKindPath, PinKindSymbol, PinKindCommand:
		default:
			return nil, errInvalidPinKind
		}
		path := strings.TrimSpace(p.Path)
		hash := normalizeHash(p.Hash)
		symbol := strings.TrimSpace(p.Symbol)
		command := strings.TrimSpace(p.Command)
		if len(path) > maxPinPathLen {
			return nil, fmt.Errorf("ledger: evidence_pins[%d] path exceeds %d bytes", i, maxPinPathLen)
		}
		if len(hash) > maxPinHashLen {
			return nil, fmt.Errorf("ledger: evidence_pins[%d] hash exceeds %d bytes", i, maxPinHashLen)
		}
		if len(symbol) > maxPinSymbolLen {
			return nil, fmt.Errorf("ledger: evidence_pins[%d] symbol exceeds %d bytes", i, maxPinSymbolLen)
		}
		if len(command) > maxPinCommandLen {
			return nil, fmt.Errorf("ledger: evidence_pins[%d] command exceeds %d bytes", i, maxPinCommandLen)
		}
		switch kind {
		case PinKindPath:
			if path == "" {
				return nil, errEmptyPinPath
			}
		case PinKindSymbol:
			if path == "" {
				return nil, errEmptyPinPath
			}
			if symbol == "" {
				return nil, errEmptyPinSymbol
			}
		case PinKindCommand:
			if command == "" {
				return nil, errEmptyPinCommand
			}
		}
		key := kind + "\x00" + path + "\x00" + symbol + "\x00" + command
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, EvidencePin{
			Kind:    kind,
			Path:    path,
			Hash:    hash,
			Symbol:  symbol,
			Command: command,
		})
	}
	return out, nil
}
