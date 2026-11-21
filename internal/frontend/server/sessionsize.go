package server

// Per-session history size for the session list (#1502). The cockpit warns
// before resuming a session that is both stale and large; "large" needs a
// size per list row, which the list did not carry.
//
// The size is the context occupancy the engine itself recorded on the
// session's most recent usage.reported event (`used`: input + cache read +
// cache creation + output of the last model request), i.e. roughly what the
// next turn re-sends. Nothing is estimated: a session with no such event in
// the tail of its log reports 0 (omitted), and the cockpit never warns for it.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"

	"github.com/jonathanung/strike/pkg/protocol"
)

// historyTailBytes bounds how much of a log is read per session. The last
// usage.reported of a turn sits near the end; a session whose tail is one
// giant line simply reports unknown.
const historyTailBytes = 1 << 20

type historyCacheEntry struct {
	size    int64
	modTime time.Time
	tokens  int64
}

// historyCache memoises sessionHistoryTokens by log path, invalidated by
// size or mtime change, so polling the list re-reads only logs that grew.
var historyCache = struct {
	sync.Mutex
	m map[string]historyCacheEntry
}{m: map[string]historyCacheEntry{}}

// sessionHistoryTokens returns the last recorded context occupancy for
// session id from its JSONL log at path, or 0 when unknown.
func sessionHistoryTokens(path, id string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		// Deleted or replaced log: drop its entry so the cache only holds
		// sessions that still exist.
		historyCache.Lock()
		delete(historyCache.m, path)
		historyCache.Unlock()
		return 0
	}
	historyCache.Lock()
	if e, ok := historyCache.m[path]; ok && e.size == st.Size() && e.modTime.Equal(st.ModTime()) {
		historyCache.Unlock()
		return e.tokens
	}
	historyCache.Unlock()

	tokens := readHistoryTokens(path, id, st.Size())

	historyCache.Lock()
	historyCache.m[path] = historyCacheEntry{size: st.Size(), modTime: st.ModTime(), tokens: tokens}
	historyCache.Unlock()
	return tokens
}

func readHistoryTokens(path, id string, size int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	offset := size - historyTailBytes
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, size-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return 0
	}
	if offset > 0 {
		// Drop the partial first line of a tail read.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return 0
		}
	}
	return lastUsageTokens(buf, id)
}

// lastUsageTokens scans JSONL lines newest-first for the session's own last
// usage.reported with a known `used` count. Child-session usage interleaved
// in a parent log and malformed lines are skipped. A compaction recorded
// after that usage shrank the history, so the old number would overstate
// it: report unknown instead.
func lastUsageTokens(buf []byte, id string) int64 {
	marker := []byte(`"usage.reported"`)
	compacted := []byte(`"compaction.completed"`)
	for len(buf) > 0 {
		end := len(buf)
		start := bytes.LastIndexByte(buf[:end], '\n')
		var line []byte
		if start == end-1 {
			buf = buf[:start]
			continue
		}
		if start < 0 {
			line, buf = buf, nil
		} else {
			line, buf = buf[start+1:end], buf[:start]
		}
		isUsage := bytes.Contains(line, marker)
		if !isUsage && !bytes.Contains(line, compacted) {
			continue
		}
		var env protocol.Envelope
		if json.Unmarshal(bytes.TrimSpace(line), &env) != nil {
			continue
		}
		ev, err := env.Decode()
		if err != nil {
			continue
		}
		if c, ok := ev.(protocol.CompactionCompleted); ok {
			if ownSessionCorrelation(id, c.Correlation) {
				return 0
			}
			continue
		}
		u, ok := ev.(protocol.UsageReported)
		if !ok || !ownSessionCorrelation(id, u.Correlation) {
			continue
		}
		if u.Used.Known && u.Used.N > 0 {
			return int64(u.Used.N)
		}
		return 0
	}
	return 0
}

func ownSessionCorrelation(id string, c protocol.Correlation) bool {
	return c.SessionID == "" || c.SessionID == id
}
