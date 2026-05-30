package app

import "sync"

type logBuffer struct {
	mu    sync.RWMutex
	limit int
	lines []string
}

func newLogBuffer(limit int) *logBuffer {
	return &logBuffer{limit: limit}
}

func (b *logBuffer) Add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return
	}
	if len(b.lines) >= b.limit {
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

func (b *logBuffer) Lines() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
