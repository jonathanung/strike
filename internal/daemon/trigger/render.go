package trigger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike/pkg/redact"
)

const (
	maxField     = 4000
	maxPromptOut = 12000
)

var placeholder = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

func defaultPrompt(kind string) string {
	switch kind {
	case KindGitHub:
		return "GitHub {{event}} {{action}} on {{repo}} {{number}}: {{title}}\n\n{{body}}\n{{url}}"
	case KindChannel:
		return "Message from {{sender}} in {{channel}} via {{adapter}}:\n\n{{text}}"
	default:
		return "Inbound webhook {{event}}.\n\n{{body}}"
	}
}

func renderPrompt(kind, tmpl string, fields map[string]string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultPrompt(kind)
	}
	out := placeholder.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := match[2 : len(match)-2]
		if v, ok := fields[key]; ok {
			return v
		}
		return match
	})
	return truncate(out, maxPromptOut)
}

func scrub(secret, text string) string {
	if secret != "" && text != "" {
		text = strings.ReplaceAll(text, secret, redact.Placeholder)
	}
	return redact.String(text)
}

func field(secret, text string) string {
	return truncate(scrub(secret, text), maxField)
}

type eventFields struct {
	fields      map[string]string
	fingerprint string
	ignore      string
}

func webhookEvent(secret, event string, body []byte) eventFields {
	if event == "" {
		event = "webhook"
	}
	sum := sha256.Sum256(body)
	text := field(secret, string(body))
	return eventFields{
		fields: map[string]string{
			"event": field(secret, event),
			"body":  text,
			"text":  text,
		},
		fingerprint: hex.EncodeToString(sum[:]),
	}
}

func githubEvent(secret, event string, body []byte, allowed []string) (eventFields, error) {
	if event == "" {
		return eventFields{}, fmt.Errorf("github event header is required")
	}
	if event == "ping" {
		return eventFields{ignore: "ping", fields: map[string]string{"event": "ping"}}, nil
	}
	if len(allowed) == 0 {
		allowed = []string{"issues", "pull_request", "issue_comment", "pull_request_review_comment"}
	}
	if !contains(allowed, event) {
		return eventFields{ignore: "event not selected", fields: map[string]string{"event": event}}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return eventFields{}, fmt.Errorf("github payload is not json")
	}
	action := asString(payload["action"])
	repo := nested(payload, "repository")
	sender := nested(payload, "sender")
	issue := nested(payload, "issue")
	pr := nested(payload, "pull_request")
	comment := nested(payload, "comment")
	subject := issue
	if len(subject) == 0 {
		subject = pr
	}
	number := asString(subject["number"])
	title := asString(subject["title"])
	bodyText := asString(subject["body"])
	url := asString(subject["html_url"])
	commentID := ""
	if len(comment) > 0 {
		commentID = asString(comment["id"])
		if asString(comment["body"]) != "" {
			bodyText = asString(comment["body"])
		}
		if asString(comment["html_url"]) != "" {
			url = asString(comment["html_url"])
		}
	}
	repoName := asString(repo["full_name"])
	numLabel := ""
	if number != "" {
		numLabel = "#" + number
	}
	fields := map[string]string{
		"event":  field(secret, event),
		"action": field(secret, action),
		"repo":   field(secret, repoName),
		"number": field(secret, numLabel),
		"title":  field(secret, title),
		"body":   field(secret, bodyText),
		"url":    field(secret, url),
		"sender": field(secret, asString(sender["login"])),
		"text":   field(secret, bodyText),
	}
	fp := strings.Join([]string{event, action, repoName, number, commentID}, "\n")
	sum := sha256.Sum256([]byte(fp))
	return eventFields{fields: fields, fingerprint: hex.EncodeToString(sum[:])}, nil
}

func channelEvent(secret string, msg Message) eventFields {
	text := field(secret, msg.Text)
	sum := sha256.Sum256([]byte(strings.Join([]string{msg.Adapter, msg.Channel, msg.MessageID, msg.Text}, "\n")))
	return eventFields{
		fields: map[string]string{
			"event":   "channel",
			"adapter": field(secret, msg.Adapter),
			"channel": field(secret, msg.Channel),
			"sender":  field(secret, msg.Sender),
			"text":    text,
			"body":    text,
		},
		fingerprint: hex.EncodeToString(sum[:]),
	}
}

func nested(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	if v == nil {
		return map[string]any{}
	}
	return v
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return ""
		}
		if t == math.Trunc(t) && t <= math.MaxInt64 && t >= math.MinInt64 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
