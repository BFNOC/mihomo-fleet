package app

import (
	"strconv"
	"sync"
	"testing"
)

func TestLogBufferAddEvictsOldestLinesInOrder(t *testing.T) {
	buf := newLogBuffer(3)
	for i := 1; i <= 5; i++ {
		buf.Add("line" + strconv.Itoa(i))
	}
	got := buf.Lines()
	want := []string{"line3", "line4", "line5"}
	if len(got) != len(want) {
		t.Fatalf("Lines() = %v, want %v", got, want)
	}
	for i, line := range want {
		if got[i] != line {
			t.Fatalf("Lines()[%d] = %q, want %q (full: %v)", i, got[i], line, got)
		}
	}
}

func TestLogBufferConcurrentAddAndLines(t *testing.T) {
	buf := newLogBuffer(100)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			buf.Add("writer:" + strconv.Itoa(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = buf.Lines()
		}
	}()
	wg.Wait()
	if len(buf.Lines()) == 0 {
		t.Fatal("expected some lines to have been recorded")
	}
}
