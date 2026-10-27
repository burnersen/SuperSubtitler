//go:build windows

package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseEvent(t *testing.T) {
	if ev, ok := parseEvent(` {"ev":"progress","index":1,"pct":0.5} `); !ok || ev["ev"] != "progress" {
		t.Errorf("event not recognised: %v %v", ev, ok)
	}
	for _, line := range []string{"", "plain text", `{"no":"ev"}`, `{broken`} {
		if _, ok := parseEvent(line); ok {
			t.Errorf("%q must not count as an event", line)
		}
	}
}

func TestToLogLineKeepsColoursOnly(t *testing.T) {
	line, ok := toLogLine("\x1b[2K\x1b[1A\x1b[96mhello\x1b[0m   ")
	if !ok || line.Text != "\x1b[96mhello\x1b[0m" {
		t.Errorf("got %q, %v", line.Text, ok)
	}
	if _, ok := toLogLine("\x1b[0m   "); ok {
		t.Error("a line with nothing visible must be dropped")
	}
}

func TestSplitLinesAndReturns(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("a\rb\nc"))
	sc.Split(splitLinesAndReturns)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if strings.Join(got, "|") != "a|b|c" {
		t.Errorf("got %q", got)
	}
}
