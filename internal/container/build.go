package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildOpts configures Manager.Build / CLI image build.
type BuildOpts struct {
	// NoCache passes --no-cache to the engine.
	NoCache bool
	// Tag is the image tag (default strike-dev:<hash[:12]>).
	Tag string
	// Progress receives plain-text build lines when non-nil.
	Progress func(line string)
}

// BuildImage builds an image from dockerfile body via `docker build`.
// Returns the image id (sha256:…) when inspectable, else the tag.
func (c *CLI) BuildImage(ctx context.Context, dockerfile string, opts BuildOpts) (imageRef string, err error) {
	tag := opts.Tag
	if tag == "" {
		tag = "strike-dev:latest"
	}
	dir, err := os.MkdirTemp("", "strike-build-ctx-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return "", err
	}

	args := []string{"build", "-t", tag, "-f", filepath.Join(dir, "Dockerfile")}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	args = append(args, dir)

	bin, err := c.Resolve()
	if err != nil {
		return "", err
	}
	stdout, stderr, code, err := c.execFn()(ctx, bin, args...)
	if opts.Progress != nil {
		for _, line := range strings.Split(stdout+stderr, "\n") {
			if strings.TrimSpace(line) != "" {
				opts.Progress(line)
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("container: build: %w", err)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		return "", fmt.Errorf("container: build: exit %d: %s", code, msg)
	}
	id, ierr := c.ImageID(ctx, tag)
	if ierr == nil && id != "" {
		return id, nil
	}
	return tag, nil
}

// ImageID returns the image id for a name/tag, or error if missing.
func (c *CLI) ImageID(ctx context.Context, name string) (string, error) {
	stdout, stderr, code, err := c.run(ctx, "image", "inspect", "--format", "{{.Id}}", name)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("container: image inspect: %s", strings.TrimSpace(stderr))
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", fmt.Errorf("container: image inspect: empty id")
	}
	return id, nil
}

// ImageExists reports whether name resolves locally.
func (c *CLI) ImageExists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	_, err := c.ImageID(ctx, name)
	return err == nil
}

// RemoveImage force-removes an image by id or tag.
func (c *CLI) RemoveImage(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, stderr, code, err := c.run(ctx, "rmi", "-f", name)
	if err != nil {
		return fmt.Errorf("container: rmi: %w", err)
	}
	if code != 0 {
		if strings.Contains(strings.ToLower(stderr), "no such") {
			return nil
		}
		return fmt.Errorf("container: rmi: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// EnsureNetwork creates a bridge network if missing.
func (c *CLI) EnsureNetwork(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("container: empty network name")
	}
	_, _, code, err := c.run(ctx, "network", "inspect", name)
	if err == nil && code == 0 {
		return nil
	}
	_, stderr, code, err := c.run(ctx, "network", "create", "--label", LabelManaged+"=true", name)
	if err != nil {
		return fmt.Errorf("container: network create: %w", err)
	}
	if code != 0 {
		if strings.Contains(strings.ToLower(stderr), "already") {
			return nil
		}
		return fmt.Errorf("container: network create: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// RemoveNetwork removes a user network by name.
func (c *CLI) RemoveNetwork(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, stderr, code, err := c.run(ctx, "network", "rm", name)
	if err != nil {
		return fmt.Errorf("container: network rm: %w", err)
	}
	if code != 0 {
		low := strings.ToLower(stderr)
		if strings.Contains(low, "not found") || strings.Contains(low, "no such") {
			return nil
		}
		return fmt.Errorf("container: network rm: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// InspectState holds a subset of container inspect fields.
type InspectState struct {
	ID      string
	Name    string
	Running bool
	Status  string
	Image   string
	Labels  map[string]string
}

// InspectContainer returns state for nameOrID or ErrNoContainer.
func (c *CLI) InspectContainer(ctx context.Context, nameOrID string) (*InspectState, error) {
	if nameOrID == "" {
		return nil, ErrNoContainer
	}
	format := "{{.Id}}|{{.Name}}|{{.State.Running}}|{{.State.Status}}|{{.Config.Image}}"
	stdout, stderr, code, err := c.run(ctx, "inspect", "--format", format, nameOrID)
	if err != nil {
		return nil, fmt.Errorf("container: inspect: %w", err)
	}
	if code != 0 {
		msg := strings.ToLower(stderr)
		if strings.Contains(msg, "no such") {
			return nil, fmt.Errorf("%w: %s", ErrNoContainer, nameOrID)
		}
		return nil, fmt.Errorf("container: inspect: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	parts := strings.Split(strings.TrimSpace(stdout), "|")
	if len(parts) < 5 {
		return nil, fmt.Errorf("container: inspect: unexpected format %q", stdout)
	}
	st := &InspectState{
		ID:     parts[0],
		Name:   strings.TrimPrefix(parts[1], "/"),
		Status: parts[3],
		Image:  parts[4],
	}
	st.Running = parts[2] == "true"
	labOut, _, labCode, _ := c.run(ctx, "inspect", "--format", "{{json .Config.Labels}}", nameOrID)
	if labCode == 0 {
		st.Labels = parseLabelJSON(strings.TrimSpace(labOut))
	}
	return st, nil
}

func parseLabelJSON(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "{}" {
		return nil
	}
	out := make(map[string]string)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if s == "" {
		return out
	}
	var cur strings.Builder
	inStr := false
	escape := false
	var parts []string
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escape {
			cur.WriteByte(ch)
			escape = false
			continue
		}
		if ch == '\\' {
			cur.WriteByte(ch)
			escape = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			cur.WriteByte(ch)
			continue
		}
		if ch == ',' && !inStr {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		idx := strings.Index(p, ":")
		if idx < 0 {
			continue
		}
		k := unquoteJSON(strings.TrimSpace(p[:idx]))
		v := unquoteJSON(strings.TrimSpace(p[idx+1:]))
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func unquoteJSON(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}
