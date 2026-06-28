package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fermat-tech/posh/internal/eval"
)

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

// TestHistoryRoundTrip saves then loads and confirms the entries survive.
func TestHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".posh_history")
	src := eval.New("posh")
	src.History = []string{"one", "two", "three"}
	saveHistory(path, src)

	got := loadHistory(path)
	if len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Fatalf("round-trip history = %v", got)
	}
}

// TestHistoryMultilineRoundTrip is the regression for multi-line commands
// (heredocs, quoted strings spanning lines): each must survive save+load as a
// single entry, not be split into separate lines.
func TestHistoryMultilineRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".posh_history")
	src := eval.New("posh")
	src.History = []string{
		"echo start",
		"cat << EOF\nline one\nline two\nEOF",
		`echo "a` + "\n" + `b"`,
		"echo end",
	}
	saveHistory(path, src)

	got := loadHistory(path)
	if len(got) != len(src.History) {
		t.Fatalf("got %d entries, want %d: %q", len(got), len(src.History), got)
	}
	for i := range src.History {
		if got[i] != src.History[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], src.History[i])
		}
	}
	// The heredoc entry must still contain its embedded newlines.
	if !strings.Contains(got[1], "\nline one\nline two\n") {
		t.Fatalf("heredoc entry lost its newlines: %q", got[1])
	}
}

func TestEncodeDecodeHistoryLine(t *testing.T) {
	cases := []string{
		"plain",
		"with\nnewline",
		"two\nembedded\nnewlines",
		`literal backslash \ and \n sequence`,
		"trailing\n",
	}
	for _, s := range cases {
		if got := decodeHistoryLine(encodeHistoryLine(s)); got != s {
			t.Errorf("round-trip %q -> %q", s, got)
		}
		// The encoded form must be a single physical line.
		if strings.ContainsAny(encodeHistoryLine(s), "\n\r") {
			t.Errorf("encoded %q still contains a raw newline", s)
		}
	}
}
