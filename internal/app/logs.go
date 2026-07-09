package app

import "sync"

// logBuffer is a fixed-capacity ring buffer of the most recent log lines.
// Add is O(1): once full, it overwrites the oldest entry in place instead of
// shifting every remaining element down by one (see M2 in
// REVIEW-2026-07-04.md -- the previous slice-shift implementation did an
// O(limit) copy under the mutex on every Add once the buffer filled, which
// for a 1000-line limit meant ~999 string-header moves per log line across
// every running instance). Lines() still returns entries oldest-first,
// matching the previous behavior.
type logBuffer struct {
	mu    sync.RWMutex
	limit int
	lines []string
	head  int // index of the oldest line in lines
	count int // number of valid lines currently stored
}

func newLogBuffer(limit int) *logBuffer {
	b := &logBuffer{limit: limit}
	if limit > 0 {
		b.lines = make([]string, limit)
	}
	return b
}

func (b *logBuffer) Add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return
	}
	if b.count < b.limit {
		b.lines[(b.head+b.count)%b.limit] = line
		b.count++
		return
	}
	// Full: overwrite the oldest slot and advance head, in place -- no
	// shifting of the other entries.
	b.lines[b.head] = line
	b.head = (b.head + 1) % b.limit
}

func (b *logBuffer) Lines() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, b.count)
	for i := 0; i < b.count; i++ {
		out[i] = b.lines[(b.head+i)%b.limit]
	}
	return out
}
