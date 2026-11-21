package deploywatch

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Preset is the "Deploy watch" trigger preset (#1783): the workspace-owned
// half of a watch. The signed deploy event supplies only which commit
// shipped; checks, including shell commands, come from here and never from
// the event body.
type Preset struct {
	// Repo, when set, pins the watched repository; an event naming another
	// repo is refused.
	Repo             string      `json:"repo,omitempty"`
	Environment      string      `json:"environment,omitempty"`
	Checks           []CheckSpec `json:"checks"`
	WindowSeconds    int         `json:"windowSeconds,omitempty"`
	IntervalSeconds  int         `json:"intervalSeconds,omitempty"`
	FailureThreshold int         `json:"failureThreshold,omitempty"`
}

// DeployEvent is the deploy webhook body: which merged commit shipped where.
type DeployEvent struct {
	Repo        string `json:"repo"`
	SHA         string `json:"sha"`
	PR          int    `json:"pr"`
	Environment string `json:"environment"`
}

// presetProbeSHA stands in for the event commit when validating a preset.
const presetProbeSHA = "0000000"

// Normalize validates p with the same rules as a watch and returns it with
// trimmed fields and defaulted check names.
func (p Preset) Normalize() (Preset, error) {
	repo := strings.TrimSpace(p.Repo)
	probe := repo
	if probe == "" {
		probe = "owner/repo"
	}
	w, err := normalize(Input{
		Repo:             probe,
		SHA:              presetProbeSHA,
		Environment:      p.Environment,
		Checks:           p.Checks,
		WindowSeconds:    p.WindowSeconds,
		IntervalSeconds:  p.IntervalSeconds,
		FailureThreshold: p.FailureThreshold,
	})
	if err != nil {
		return Preset{}, err
	}
	return Preset{
		Repo:             repo,
		Environment:      w.Environment,
		Checks:           w.Checks,
		WindowSeconds:    p.WindowSeconds,
		IntervalSeconds:  p.IntervalSeconds,
		FailureThreshold: p.FailureThreshold,
	}, nil
}

// ParseDeployEvent decodes a deploy webhook body. Unknown fields are
// ignored so CD systems can send richer payloads.
func ParseDeployEvent(body []byte) (DeployEvent, error) {
	var ev DeployEvent
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&ev); err != nil {
		return DeployEvent{}, invalid("deploy event must be a JSON object with sha (and repo, pr, environment)")
	}
	ev.Repo = strings.TrimSpace(ev.Repo)
	ev.SHA = strings.TrimSpace(ev.SHA)
	ev.Environment = strings.TrimSpace(ev.Environment)
	if ev.SHA == "" {
		return DeployEvent{}, invalid("deploy event needs sha")
	}
	return ev, nil
}

// Input merges a deploy event into the preset. The event's environment wins
// over the preset's default.
func (p Preset) Input(ev DeployEvent) (Input, error) {
	repo := ev.Repo
	switch {
	case p.Repo != "" && repo == "":
		repo = p.Repo
	case p.Repo != "" && !strings.EqualFold(repo, p.Repo):
		return Input{}, invalid("deploy event repo does not match this trigger")
	case repo == "":
		return Input{}, invalid("deploy event needs repo")
	}
	env := ev.Environment
	if env == "" {
		env = p.Environment
	}
	return Input{
		Repo:             repo,
		SHA:              ev.SHA,
		PR:               ev.PR,
		Environment:      env,
		Checks:           append([]CheckSpec(nil), p.Checks...),
		WindowSeconds:    p.WindowSeconds,
		IntervalSeconds:  p.IntervalSeconds,
		FailureThreshold: p.FailureThreshold,
	}, nil
}
