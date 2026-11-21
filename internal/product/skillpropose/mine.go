// Package skillpropose finds workflows the operator repeats across sessions
// and drafts each one as a skill proposal (#1611). Proposals stay pending
// until the operator accepts, edits, or dismisses them; nothing is installed
// here and no session can see a draft.
//
// Mining is local and model-free: it reads the workspace's own root session
// logs, takes each session's opening request and the tool sequence of its
// first turn, and groups sessions whose openings share most of their words
// and whose tool sequences mostly match. A group needs Threshold distinct
// sessions inside Window to become a proposal. Every piece of transcript
// text that reaches a draft goes through pkg/redact first.
package skillpropose

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

// Defaults for the open questions in #1611: three similar workflows in 28
// days. The threshold is configurable per workspace.
const (
	DefaultThreshold = 3
	DefaultWindow    = 28 * 24 * time.Hour
	MinThreshold     = 2
	MaxThreshold     = 20

	// minTools is the shortest tool sequence that counts as a multi-step
	// workflow. A one-tool turn is not worth a skill.
	minTools = 2
	// Similarity floors for joining a group.
	minOpeningOverlap = 0.5
	minToolOverlap    = 0.6
	maxOpeningRunes   = 160
)

// Workflow is one root session's opening request and first-turn tools.
type Workflow struct {
	SessionID string
	Title     string
	At        time.Time
	Opening   string // redacted
	words     []string
	Tools     []string
}

// Evidence names one session a proposal was mined from.
type Evidence struct {
	SessionID string    `json:"sessionId"`
	Title     string    `json:"title,omitempty"`
	At        time.Time `json:"at"`
}

// Proposal is a drafted skill waiting for the operator.
type Proposal struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Body        string     `json:"body"`
	Tools       []string   `json:"tools"`
	Count       int        `json:"count"`
	Sessions    []Evidence `json:"sessions"`
	DraftedBy   string     `json:"draftedBy"`
	CreatedAt   time.Time  `json:"createdAt"`
}

var lineLimit = 32 << 20

// ReadWorkflows reads root session logs in dir that belong to projectKey and
// whose opening request is at or after since. Child sessions, sessions of
// other workspaces, and sessions without a multi-step first turn are
// skipped.
func ReadWorkflows(dir, projectKey string, since time.Time) ([]Workflow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Workflow
	for _, ent := range entries {
		name := ent.Name()
		if !ent.Type().IsRegular() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		meta, err := session.ReadMeta(dir, id)
		if err != nil || meta.ParentSessionID != "" {
			continue
		}
		if strings.TrimSpace(projectKey) == "" || meta.ProjectKey != projectKey {
			continue
		}
		wf, ok, err := readWorkflow(filepath.Join(dir, name), id)
		if err != nil {
			return nil, err
		}
		if !ok || wf.At.Before(since) {
			continue
		}
		wf.Title = redact.String(meta.Title)
		out = append(out, wf)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

func readWorkflow(path, id string) (Workflow, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Workflow{}, false, nil
		}
		return Workflow{}, false, err
	}
	defer f.Close()
	wf := Workflow{SessionID: id}
	opened := false
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, tooLong, err := readCappedLine(br, lineLimit)
		if !tooLong {
			if done := applyLine(strings.TrimSpace(raw), id, &wf, &opened); done {
				break
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Workflow{}, false, err
		}
	}
	if !opened || len(wf.Tools) < minTools || len(wf.words) == 0 {
		return Workflow{}, false, nil
	}
	return wf, true, nil
}

// applyLine folds one envelope into wf and reports when the first turn has
// completed.
func applyLine(line, id string, wf *Workflow, opened *bool) bool {
	if line == "" {
		return false
	}
	var env protocol.Envelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return false
	}
	switch env.Type {
	case "user.message":
		if *opened {
			return false
		}
		var ev protocol.UserMessage
		if json.Unmarshal(env.Data, &ev) != nil || !ownEvent(ev.SessionID, id) {
			return false
		}
		text := strings.TrimSpace(redact.String(ev.Text))
		if text == "" {
			return false
		}
		*opened = true
		wf.At = env.Time.UTC()
		wf.Opening = clip(oneLine(text), maxOpeningRunes)
		wf.words = words(text)
	case "tool.begin":
		if !*opened {
			return false
		}
		var ev protocol.ToolCallBegin
		if json.Unmarshal(env.Data, &ev) != nil || !ownEvent(ev.SessionID, id) {
			return false
		}
		name := strings.TrimSpace(ev.Name)
		if name == "" {
			return false
		}
		if n := len(wf.Tools); n == 0 || wf.Tools[n-1] != name {
			wf.Tools = append(wf.Tools, name)
		}
	case "turn.completed":
		if !*opened {
			return false
		}
		var ev protocol.TurnCompleted
		if json.Unmarshal(env.Data, &ev) == nil && ownEvent(ev.SessionID, id) {
			return true
		}
	}
	return false
}

// ownEvent drops child events copied onto a parent log.
func ownEvent(eventSession, fileID string) bool {
	s := strings.TrimSpace(eventSession)
	return s == "" || s == fileID
}

var stopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(`the and for with that this from into onto then than
		please can you could would should will just also what when where which who how why
		are was were been being have has had does did doing make sure about there their them
		they its it's our your mine all any some each every more most other such only own same
		very too out off over under again further once here both few nor not yet let lets get
		got use using used want need like now new one two redacted redacted_high_entropy`) {
		stopwords[w] = struct{}{}
	}
}

// words returns the distinct content words of text, in first-seen order.
// Words with digits or longer than 20 runes are dropped: they are ids,
// hashes, or token-shaped strings, never good skill names.
func words(text string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len([]rune(w)) < 3 || len([]rune(w)) > 20 {
			continue
		}
		if strings.IndexFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) >= 0 {
			continue
		}
		if _, stop := stopwords[w]; stop {
			continue
		}
		if _, dup := seen[w]; dup {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	return out
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(a))
	for _, w := range a {
		set[w] = struct{}{}
	}
	inter := 0
	for _, w := range b {
		if _, ok := set[w]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// seqOverlap is 2·LCS / (len a + len b): 1 for identical sequences.
func seqOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return 2 * float64(prev[len(b)]) / float64(len(a)+len(b))
}

// Similar reports whether two workflows are the same repeated workflow.
func Similar(a, b Workflow) bool {
	return jaccard(a.words, b.words) >= minOpeningOverlap && seqOverlap(a.Tools, b.Tools) >= minToolOverlap
}

// Mine groups similar workflows and drafts a proposal for every group of at
// least threshold distinct sessions. Each workflow joins the first group
// whose seed it is similar to, so a near miss never inflates a group.
func Mine(wfs []Workflow, threshold int, now time.Time) []Proposal {
	if threshold < MinThreshold {
		threshold = MinThreshold
	}
	var groups [][]Workflow
	for _, wf := range wfs {
		placed := false
		for i := range groups {
			if Similar(groups[i][0], wf) {
				groups[i] = append(groups[i], wf)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []Workflow{wf})
		}
	}
	var out []Proposal
	for _, g := range groups {
		if len(g) >= threshold {
			out = append(out, draft(g, now))
		}
	}
	return out
}

// draft writes the SKILL.md parts for one group. The seed (oldest) session's
// opening and tools stand for the group.
func draft(g []Workflow, now time.Time) Proposal {
	seed := g[0]
	common := commonWords(g)
	ev := make([]Evidence, 0, len(g))
	for _, wf := range g {
		ev = append(ev, Evidence{SessionID: wf.SessionID, Title: wf.Title, At: wf.At})
	}
	name := skillName(common, seed.words)
	steps := make([]string, 0, len(seed.Tools))
	for i, t := range seed.Tools {
		steps = append(steps, fmt.Sprintf("%d. Use the `%s` tool.", i+1, t))
	}
	body := strings.Join([]string{
		fmt.Sprintf("Drafted from %d similar sessions in this workspace. Review the steps before you rely on them.", len(g)),
		"",
		"## When to use",
		"",
		"Requests like: " + seed.Opening,
		"",
		"## Steps",
		"",
		strings.Join(steps, "\n"),
		"",
		"## Request",
		"",
		"$ARGUMENTS",
	}, "\n")
	return Proposal{
		ID:          patternID(common, seed.Tools),
		Name:        name,
		Description: redact.String(clip("Repeated workflow: "+seed.Opening, 200)),
		Body:        redact.String(body),
		Tools:       append([]string(nil), seed.Tools...),
		Count:       len(g),
		Sessions:    ev,
		DraftedBy:   "local pattern match (no model call)",
		CreatedAt:   now.UTC(),
	}
}

// commonWords are the seed's words that appear in most of the group, in the
// seed's order.
func commonWords(g []Workflow) []string {
	var out []string
	for _, w := range g[0].words {
		n := 0
		for _, wf := range g {
			for _, x := range wf.words {
				if x == w {
					n++
					break
				}
			}
		}
		if n*2 > len(g) {
			out = append(out, w)
		}
	}
	return out
}

func skillName(common, fallback []string) string {
	src := common
	if len(src) == 0 {
		src = fallback
	}
	if len(src) > 3 {
		src = src[:3]
	}
	switch len(src) {
	case 0:
		return "repeated-workflow"
	case 1:
		// Single words collide with reserved skill names ("model",
		// "agent", …) that Accept would refuse.
		return src[0] + "-workflow"
	}
	return strings.Join(src, "-")
}

// patternID keys a pattern for dismissal: the common words (sorted) and the
// tool sequence. Small wording changes keep the same key.
func patternID(common, tools []string) string {
	ws := append([]string(nil), common...)
	sort.Strings(ws)
	sum := sha256.Sum256([]byte(strings.Join(ws, " ") + "|" + strings.Join(tools, ">")))
	return hex.EncodeToString(sum[:])[:16]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func readCappedLine(r *bufio.Reader, max int) (string, bool, error) {
	var b strings.Builder
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if b.Len()+len(chunk) > max {
				tooLong = true
				b.Reset()
			} else {
				b.Write(chunk)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return "", true, err
		}
		return b.String(), false, err
	}
}
