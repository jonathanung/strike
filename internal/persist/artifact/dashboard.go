package artifact

// Agent-authored dashboards (#1628, successor to #1444's session_report).
//
// A dashboard is an ordinary artifact of type "dashboard" whose content is
// a JSON page spec: a title-less list of typed blocks. Provenance is a
// property of the block kind, not a label the agent types:
//
//   - measured kinds ("metric", "sessions_kanban") name what to measure;
//     the daemon fills the value from its own records at read time. The
//     spec may not carry a value for them — a metric block with a "value"
//     (or any unknown field) is rejected, so an agent cannot plant a number
//     that renders as measured.
//   - agent-written kinds ("text", "list", "kanban") carry the agent's own
//     content and always render with an "agent wrote this" marker.
//
// Edits go through the normal artifact CAS update, so a save replaces the
// whole spec atomically (all-or-nothing per save).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// TypeDashboard is an agent-authored project page (#1628).
const TypeDashboard = "dashboard"

// Dashboard block kinds.
const (
	BlockMetric         = "metric"
	BlockSessionsKanban = "sessions_kanban"
	BlockText           = "text"
	BlockList           = "list"
	BlockKanban         = "kanban"
)

const (
	dashMaxBlocks  = 40
	dashMaxTitle   = 120
	dashMaxBody    = 4000
	dashMaxItems   = 50
	dashMaxItem    = 300
	dashMaxColumns = 8
	dashMaxCards   = 50
	dashMaxMetric  = 64
)

// DashboardSpec is the parsed content of a dashboard artifact.
type DashboardSpec struct {
	Version  int              `json:"version"`
	Template string           `json:"template,omitempty"`
	Blocks   []DashboardBlock `json:"blocks"`
}

// DashboardBlock is one block. Which fields apply depends on Kind.
type DashboardBlock struct {
	Kind    string            `json:"kind"`
	Title   string            `json:"title,omitempty"`
	Metric  string            `json:"metric,omitempty"`
	Body    string            `json:"body,omitempty"`
	Items   []string          `json:"items,omitempty"`
	Columns []DashboardColumn `json:"columns,omitempty"`
}

// DashboardColumn is one agent-written kanban column.
type DashboardColumn struct {
	Title string   `json:"title"`
	Cards []string `json:"cards"`
}

// MeasuredBlock reports whether the daemon fills kind from its records.
func MeasuredBlock(kind string) bool {
	return kind == BlockMetric || kind == BlockSessionsKanban
}

var errDashboard = errors.New("artifact: invalid dashboard")

func dashErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errDashboard, fmt.Sprintf(format, args...))
}

func dashText(field, s string, max int) error {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return dashErr("%s is longer than %d characters", field, max)
	}
	return nil
}

func validMetricName(s string) bool {
	if s == "" || len(s) > dashMaxMetric {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}

// ParseDashboard decodes and validates a dashboard spec. Unknown fields are
// rejected (this is what keeps values out of measured blocks).
func ParseDashboard(content string) (DashboardSpec, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(content)))
	dec.DisallowUnknownFields()
	var spec DashboardSpec
	if err := dec.Decode(&spec); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "unknown field") {
			return DashboardSpec{}, dashErr("%s (measured blocks name a metric; the daemon fills the value)", msg)
		}
		return DashboardSpec{}, dashErr("content must be a JSON page spec: %s", msg)
	}
	if dec.More() {
		return DashboardSpec{}, dashErr("content has trailing data")
	}
	if spec.Version == 0 {
		spec.Version = 1
	}
	if spec.Version != 1 {
		return DashboardSpec{}, dashErr("version must be 1")
	}
	if spec.Template != "" && !validMetricName(spec.Template) {
		return DashboardSpec{}, dashErr("template must be a short [a-z0-9._] name")
	}
	if len(spec.Blocks) == 0 || len(spec.Blocks) > dashMaxBlocks {
		return DashboardSpec{}, dashErr("a page needs 1-%d blocks", dashMaxBlocks)
	}
	for i, b := range spec.Blocks {
		at := fmt.Sprintf("block %d", i+1)
		if err := dashText(at+" title", b.Title, dashMaxTitle); err != nil {
			return DashboardSpec{}, err
		}
		switch b.Kind {
		case BlockMetric:
			if !validMetricName(b.Metric) {
				return DashboardSpec{}, dashErr("%s: metric must name a daemon metric like sessions.total", at)
			}
			if b.Body != "" || len(b.Items) > 0 || len(b.Columns) > 0 {
				return DashboardSpec{}, dashErr("%s: a metric block carries only kind, title and metric", at)
			}
		case BlockSessionsKanban:
			if b.Metric != "" || b.Body != "" || len(b.Items) > 0 || len(b.Columns) > 0 {
				return DashboardSpec{}, dashErr("%s: sessions_kanban is filled by the daemon; give it only a title", at)
			}
		case BlockText:
			if b.Metric != "" || len(b.Items) > 0 || len(b.Columns) > 0 {
				return DashboardSpec{}, dashErr("%s: a text block carries title and body", at)
			}
			if err := dashText(at+" body", b.Body, dashMaxBody); err != nil {
				return DashboardSpec{}, err
			}
		case BlockList:
			if b.Metric != "" || b.Body != "" || len(b.Columns) > 0 {
				return DashboardSpec{}, dashErr("%s: a list block carries title and items", at)
			}
			if len(b.Items) > dashMaxItems {
				return DashboardSpec{}, dashErr("%s: at most %d items", at, dashMaxItems)
			}
			for _, it := range b.Items {
				if err := dashText(at+" item", it, dashMaxItem); err != nil {
					return DashboardSpec{}, err
				}
			}
		case BlockKanban:
			if b.Metric != "" || b.Body != "" || len(b.Items) > 0 {
				return DashboardSpec{}, dashErr("%s: a kanban block carries title and columns", at)
			}
			if len(b.Columns) == 0 || len(b.Columns) > dashMaxColumns {
				return DashboardSpec{}, dashErr("%s: a kanban needs 1-%d columns", at, dashMaxColumns)
			}
			for _, c := range b.Columns {
				if err := dashText(at+" column title", c.Title, dashMaxTitle); err != nil {
					return DashboardSpec{}, err
				}
				if len(c.Cards) > dashMaxCards {
					return DashboardSpec{}, dashErr("%s: at most %d cards per column", at, dashMaxCards)
				}
				for _, card := range c.Cards {
					if err := dashText(at+" card", card, dashMaxItem); err != nil {
						return DashboardSpec{}, err
					}
				}
			}
		default:
			return DashboardSpec{}, dashErr("%s: unknown kind %q (metric, sessions_kanban, text, list, kanban)", at, b.Kind)
		}
	}
	return spec, nil
}

// validateTypedContent applies per-type content rules on create/update.
func validateTypedContent(typ, content string) error {
	if typ == TypeDashboard {
		_, err := ParseDashboard(content)
		return err
	}
	return nil
}
