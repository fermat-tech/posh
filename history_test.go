package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fermat-tech/posh/internal/eval"
)

func TestNewlineTracker(t *testing.T) {
	var buf bytes.Buffer
	tr := &newlineTracker{w: &buf, atLineStart: true}

	io.WriteString(tr, "1 2 3 ") // partial line, no trailing newline
	if tr.atLineStart {
		t.Fatalf("atLineStart should be false after partial-line output")
	}
	io.WriteString(tr, "done\n") // ends with newline
	if !tr.atLineStart {
		t.Fatalf("atLineStart should be true after newline-terminated output")
	}
	// All bytes pass through unchanged.
	if buf.String() != "1 2 3 done\n" {
		t.Fatalf("tracker altered output: %q", buf.String())
	}
}

func TestSaveHistoryWritesAllEntries(t *testing.T) {
	sh := eval.New("posh")
	sh.History = []string{"echo a", "ls -l", "cd /tmp"}
	path := filepath.Join(t.TempDir(), ".posh_history")

	saveHistory(path, sh)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("history file not written: %v", err)
	}
	if got, want := string(data), "echo a\nls -l\ncd /tmp\n"; got != want {
		t.Fatalf("history = %q, want %q", got, want)
	}
}

func TestSaveHistoryCapsToMax(t *testing.T) {
	sh := eval.New("posh")
	for i := 0; i < maxHistory+50; i++ {
		sh.History = append(sh.History, "cmd"+strconv.Itoa(i))
	}
	path := filepath.Join(t.TempDir(), ".posh_history")

	saveHistory(path, sh)

	f, _ := os.Open(path)
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != maxHistory {
		t.Fatalf("kept %d lines, want %d", len(lines), maxHistory)
	}
	// The most recent entries are the ones retained.
	if lines[len(lines)-1] != "cmd"+strconv.Itoa(maxHistory+49) {
		t.Fatalf("last entry = %q, want the most recent command", lines[len(lines)-1])
	}
}

// TestHistoryRoundTrip mirrors the REPL's load step (read file into sh.History)
// and confirms a save then load preserves the entries.
func TestHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".posh_history")
	src := eval.New("posh")
	src.History = []string{"one", "two", "three"}
	saveHistory(path, src)

	// Load the way runREPL does.
	dst := eval.New("posh")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			dst.History = append(dst.History, line)
		}
	}
	f.Close()

	if len(dst.History) != 3 || dst.History[0] != "one" || dst.History[2] != "three" {
		t.Fatalf("round-trip history = %v", dst.History)
	}
}
