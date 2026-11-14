package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike/pkg/redact"
)

// maxRunOutput bounds one sidecar file. Bounded retention is a safety
// property, not only a disk one: there is no unbounded full-output file.
const maxRunOutput = 64 << 10

// ErrNoOutput is returned by ReadRunOutput when the run kept no sidecar.
var ErrNoOutput = errors.New("no run output")

// runOutputPath is runs/<scheduleId>/<runId>.log under the store dir. Ids
// are generated hex, but the path is still refused for anything that could
// leave runsDir.
func (s *Store) runOutputPath(scheduleID, runID string) (string, error) {
	if !safeID(scheduleID) || !safeID(runID) {
		return "", fmt.Errorf("schedule: bad run output id %q/%q", scheduleID, runID)
	}
	return filepath.Join(s.runsDir, scheduleID, runID+".log"), nil
}

func safeID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\\x00")
}

// WriteRunOutput stores the scrubbed tail of a run's combined output as the
// run's sidecar (directory 0700, file 0600). Output beyond maxRunOutput is
// dropped from the front and the first line says how much. It returns the
// OutputRef to record on the run.
func (s *Store) WriteRunOutput(scheduleID, runID string, output []byte) (string, error) {
	path, err := s.runOutputPath(scheduleID, runID)
	if err != nil {
		return "", err
	}
	text := tailOutput(redact.ScrubToolOutput(string(output)), maxRunOutput)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return runID, nil
}

// tailOutput keeps the end of text so the whole result, note included,
// fits in limit bytes. The cut lands on a rune boundary.
func tailOutput(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	dropped := len(text) - limit
	for dropped < len(text) {
		note := fmt.Sprintf("… (%d bytes dropped)\n", dropped)
		if len(note)+len(text)-dropped <= limit {
			for dropped < len(text) && !utf8.RuneStart(text[dropped]) {
				dropped++
			}
			return fmt.Sprintf("… (%d bytes dropped)\n", dropped) + text[dropped:]
		}
		dropped++
	}
	// limit is smaller than the note itself; nothing of the text fits.
	return ""
}

// ReadRunOutput returns the sidecar for runID, or ErrNoOutput.
func (s *Store) ReadRunOutput(scheduleID, runID string) ([]byte, error) {
	path, err := s.runOutputPath(scheduleID, runID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoOutput
	}
	return data, err
}

// removeDroppedOutputs deletes sidecars of runs present in prev but not cur.
func (s *Store) removeDroppedOutputs(scheduleID string, prev, cur []Run) {
	if len(prev) == 0 {
		return
	}
	kept := make(map[string]bool, len(cur))
	for _, run := range cur {
		kept[run.ID] = true
	}
	for _, run := range prev {
		if run.ID == "" || kept[run.ID] {
			continue
		}
		if path, err := s.runOutputPath(scheduleID, run.ID); err == nil {
			_ = os.Remove(path)
		}
	}
}

// removeScheduleOutputs deletes every sidecar of one schedule.
func (s *Store) removeScheduleOutputs(scheduleID string) {
	if !safeID(scheduleID) {
		return
	}
	_ = os.RemoveAll(filepath.Join(s.runsDir, scheduleID))
}

// reconcileOutputsLocked removes sidecars whose schedule or run no longer
// exists (a crash between the record write and the file write, or a cap
// trim that did not finish). It only ever deletes files this store writes
// (<runId>.log and its .tmp) and then removes a schedule dir that is left
// empty, so a misconfigured root cannot take anything else with it.
func (s *Store) reconcileOutputsLocked() {
	entries, err := os.ReadDir(s.runsDir)
	if err != nil {
		return
	}
	runsOf := make(map[string]map[string]bool, len(s.st.Schedules))
	for _, sch := range s.st.Schedules {
		ids := make(map[string]bool, len(sch.Runs))
		for _, run := range sch.Runs {
			ids[run.ID] = true
		}
		runsOf[sch.ID] = ids
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.runsDir, entry.Name())
		ids, known := runsOf[entry.Name()]
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			runID, isLog := strings.CutSuffix(name, ".log")
			if strings.HasSuffix(name, ".log.tmp") || (isLog && !ids[runID]) {
				_ = os.Remove(filepath.Join(dir, name))
			}
		}
		if !known {
			// Succeeds only when nothing foreign is left inside.
			_ = os.Remove(dir)
		}
	}
}
