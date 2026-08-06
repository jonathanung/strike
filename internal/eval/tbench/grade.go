package tbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
)

// GraderName selects how tasks are verified.
type GraderName string

const (
	// GraderDocker copies the agent workspace + tests into the task image and
	// runs tests/test.sh, reading /logs/verifier/reward.txt|json.
	GraderDocker GraderName = "docker"
	// GraderNone skips verification (agent metrics only).
	GraderNone GraderName = "none"
)

// GradeResult is the outcome of verification for one instance.
type GradeResult struct {
	Resolved bool
	Reward   float64
	Detail   string
	Duration time.Duration
	Skipped  bool
}

// Grader grades an agent workspace for one instance.
type Grader interface {
	Grade(ctx context.Context, in Instance, workDir string) (GradeResult, error)
}

// NoneGrader skips verification.
type NoneGrader struct{}

// Grade implements Grader.
func (NoneGrader) Grade(context.Context, Instance, string) (GradeResult, error) {
	return GradeResult{Skipped: true, Detail: "grader=none"}, nil
}

// DockerGrader verifies by running the Harbor test script inside the task image.
type DockerGrader struct {
	RT       swebench.Runtime
	Timeout  time.Duration // default 15m; overridden by instance VerifyTimeout
	Pull     bool
	WorkRoot string
}

// Grade implements Grader.
func (g *DockerGrader) Grade(ctx context.Context, in Instance, workDir string) (GradeResult, error) {
	start := time.Now()
	if g.RT == nil {
		return GradeResult{}, fmt.Errorf("tbench: docker grader: nil runtime")
	}
	if workDir == "" {
		return GradeResult{}, fmt.Errorf("tbench: docker grader: empty workDir")
	}
	if in.TaskDir == "" {
		return GradeResult{
			Resolved: false,
			Detail:   "no task_dir (tests unavailable)",
			Duration: time.Since(start),
		}, nil
	}
	testsDir := filepath.Join(in.TaskDir, "tests")
	if st, err := os.Stat(testsDir); err != nil || !st.IsDir() {
		return GradeResult{
			Resolved: false,
			Detail:   "missing tests/ in task pack",
			Duration: time.Since(start),
		}, nil
	}
	testSh := filepath.Join(testsDir, "test.sh")
	if _, err := os.Stat(testSh); err != nil {
		return GradeResult{
			Resolved: false,
			Detail:   "missing tests/test.sh",
			Duration: time.Since(start),
		}, nil
	}

	image := in.DockerImage
	if image == "" {
		image = DefaultImage(in.InstanceID)
	}
	if g.Pull {
		if err := g.RT.Pull(ctx, image); err != nil {
			return GradeResult{}, err
		}
	}

	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	if in.VerifyTimeout > 0 {
		timeout = time.Duration(in.VerifyTimeout * float64(time.Second))
	}

	id, err := g.RT.Create(ctx, image, swebench.CreateOpts{
		WorkDir:    WorkDirInContainer,
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return GradeResult{}, err
	}
	defer func() { _ = g.RT.Remove(context.Background(), id) }()
	if err := g.RT.Start(ctx, id); err != nil {
		return GradeResult{}, err
	}

	// Replace /app with the agent workspace. docker cp of a directory into an
	// existing path nests; remove first then copy contents.
	_, _, _, _ = g.RT.Exec(ctx, id, []string{"bash", "-lc", "rm -rf /app/* /app/.[!.]* /app/..?* 2>/dev/null; mkdir -p /app /tests /logs/verifier /logs/agent"}, swebench.ExecOpts{Timeout: 2 * time.Minute})

	// Copy workspace: host workDir/. -> container:/app/
	// docker cp requires trailing /. semantics; copy the directory then flatten if needed.
	if err := g.RT.CopyTo(ctx, id, workDir+"/.", WorkDirInContainer+"/"); err != nil {
		// Fallback without "/."
		if err2 := g.RT.CopyTo(ctx, id, workDir, WorkDirInContainer); err2 != nil {
			return GradeResult{}, fmt.Errorf("copy workspace: %v / %v", err, err2)
		}
	}
	if err := g.RT.CopyTo(ctx, id, testsDir, "/tests"); err != nil {
		return GradeResult{}, fmt.Errorf("copy tests: %w", err)
	}

	// Ensure test.sh is executable and run from /app (Harbor convention).
	stdout, stderr, code, err := g.RT.Exec(ctx, id, []string{
		"bash", "-lc",
		"chmod +x /tests/test.sh 2>/dev/null; mkdir -p /logs/verifier /logs/agent; cd /app && bash /tests/test.sh",
	}, swebench.ExecOpts{
		WorkDir: WorkDirInContainer,
		Timeout: timeout,
	})
	if err != nil {
		return GradeResult{
			Resolved: false,
			Detail:   fmt.Sprintf("test exec: %v", err),
			Duration: time.Since(start),
		}, nil
	}

	reward, rewardDetail, rerr := g.readReward(ctx, id)
	detail := fmt.Sprintf("test_exit=%d", code)
	if rewardDetail != "" {
		detail = detail + "; " + rewardDetail
	}
	if rerr != nil {
		// Fall back to exit code when reward file missing.
		if code == 0 {
			reward = 1
			detail = detail + "; reward_missing_assume_pass"
		} else {
			detail = detail + "; reward: " + rerr.Error()
			if stderr != "" {
				detail = detail + "; stderr=" + truncate(stderr, 200)
			} else if stdout != "" {
				detail = detail + "; stdout=" + truncate(stdout, 200)
			}
			return GradeResult{
				Resolved: false,
				Reward:   0,
				Detail:   detail,
				Duration: time.Since(start),
			}, nil
		}
	}

	resolved := reward > 0
	return GradeResult{
		Resolved: resolved,
		Reward:   reward,
		Detail:   detail,
		Duration: time.Since(start),
	}, nil
}

func (g *DockerGrader) readReward(ctx context.Context, containerID string) (float64, string, error) {
	// Prefer reward.json then reward.txt (Harbor order).
	for _, path := range []string{"/logs/verifier/reward.json", "/logs/verifier/reward.txt"} {
		stdout, stderr, code, err := g.RT.Exec(ctx, containerID, []string{"bash", "-lc", "cat " + path}, swebench.ExecOpts{Timeout: 30 * time.Second})
		if err != nil || code != 0 {
			_ = stderr
			continue
		}
		raw := strings.TrimSpace(stdout)
		if raw == "" {
			continue
		}
		if strings.HasSuffix(path, ".json") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(raw), &obj); err != nil {
				// Single number JSON.
				if f, err2 := strconv.ParseFloat(raw, 64); err2 == nil {
					return f, "reward.json=" + raw, nil
				}
				continue
			}
			// Common keys: reward, accuracy, score — take first numeric >-1 looking field.
			for _, key := range []string{"reward", "score", "accuracy", "pass"} {
				if v, ok := obj[key]; ok {
					if f, ok := asFloat(v); ok {
						return f, fmt.Sprintf("reward.json.%s=%v", key, v), nil
					}
				}
			}
			// Any numeric value.
			for k, v := range obj {
				if f, ok := asFloat(v); ok {
					return f, fmt.Sprintf("reward.json.%s=%v", k, v), nil
				}
			}
			continue
		}
		// reward.txt: single number.
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		return f, "reward.txt=" + raw, nil
	}
	return 0, "", fmt.Errorf("no reward file in /logs/verifier")
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// NewGrader constructs a grader by name.
func NewGrader(name GraderName, rt swebench.Runtime, workRoot string, pull bool) (Grader, error) {
	switch name {
	case "", GraderDocker:
		return &DockerGrader{RT: rt, WorkRoot: workRoot, Pull: pull}, nil
	case GraderNone:
		return NoneGrader{}, nil
	default:
		return nil, fmt.Errorf("tbench: unknown grader %q (docker|none)", name)
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
