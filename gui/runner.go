//go:build windows

// runner.go — start one SuperSubtitler run, follow it, answer it, stop it.
//
// The CLI runs with -json: stdout carries one JSON event per line, stderr the
// display meant for people, which ends up in the log. The window never decides
// anything about a file itself; it shows what the CLI reports.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// maxEventLine bounds one line; generous for long file names, but a broken
// counterpart cannot fill the memory.
const maxEventLine = 1 << 20

var (
	// nonColorEscape removes every terminal control sequence EXCEPT colours
	// (cursor moves, line clears, window titles). Colours stay: the log shows
	// them, and without them a warning reads like any other line.
	nonColorEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[@-ln-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	colorEscape    = regexp.MustCompile(`\x1b\[[0-9;]*m`)
)

// LogLine is one line for the log box.
type LogLine struct {
	Text string `json:"text"`
}

// RunState says what is running — and how a run ended.
type RunState struct {
	Running  bool   `json:"running"`
	Stopping bool   `json:"stopping"`
	Kind     string `json:"kind"` // subtitle | merge | setup
	Batch    int    `json:"batch"`
	Batches  int    `json:"batches"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error"`
}

// Runner watches exactly one CLI process at a time.
type Runner struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	pid      int
	stopping bool
	input    io.WriteCloser
	sink     func(name string, data ...any)
	onExit   func(state RunState)
	state    RunState
}

// NewRunner creates the runner; sink forwards events to the window, onExit is
// told once per finished process (the app uses it to start the next batch).
func NewRunner(sink func(name string, data ...any), onExit func(RunState)) *Runner {
	return &Runner{sink: sink, onExit: onExit}
}

// Running reports whether a process is active.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil
}

// Start launches the CLI and starts reading both channels.
func (r *Runner) Start(exePath, workDir string, args []string, state RunState) error {
	r.mu.Lock()
	if r.cmd != nil {
		r.mu.Unlock()
		return errors.New("a run is already in progress")
	}
	cmd := exec.Command(exePath, args...)
	cmd.Dir = workDir
	// Own process group so the clean-stop signal hits exactly this run.
	// CREATE_NO_WINDOW must NOT be added — see wincon.go.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("runner.go: Start (StdoutPipe): %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("runner.go: Start (StderrPipe): %w", err)
	}
	// The way back for answers. Without it the CLI would read a closed input
	// and take every default at once — no dialog would ever get a say.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("runner.go: Start (StdinPipe): %w", err)
	}
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("runner.go: Start: %w", err)
	}
	r.cmd, r.pid, r.stopping, r.input = cmd, cmd.Process.Pid, false, stdin
	state.Running = true
	r.state = state
	r.mu.Unlock()

	r.sink("run:state", state)

	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); r.readChannel(stdout, true) }()
	go func() { defer readers.Done(); r.readChannel(stderr, false) }()
	go func() {
		// Wait only after both channels are drained: Wait closes the pipes,
		// and the last lines are the most interesting ones.
		readers.Wait()
		r.finish(cmd.Wait())
	}()
	return nil
}

func (r *Runner) finish(waitErr error) {
	r.mu.Lock()
	state := r.state
	state.Running = false
	state.Stopping = r.stopping
	r.cmd, r.pid, r.stopping, r.input = nil, 0, false, nil
	r.mu.Unlock()

	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		state.ExitCode = exitErr.ExitCode()
	default:
		state.Error = waitErr.Error()
	}
	if r.onExit != nil {
		r.onExit(state)
	}
}

// readChannel reads stdout (events) or stderr (log). Anything on stdout that
// is not an event is treated as log text rather than dropped.
func (r *Runner) readChannel(pipe io.Reader, mayCarryEvents bool) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	scanner.Split(splitLinesAndReturns)
	for scanner.Scan() {
		raw := scanner.Text()
		if mayCarryEvents {
			if event, ok := parseEvent(raw); ok {
				r.sink("run:event", event)
				continue
			}
		}
		if line, ok := toLogLine(raw); ok {
			r.sink("run:log", line)
		}
	}
	if err := scanner.Err(); err != nil {
		r.sink("run:log", LogLine{Text: "[gui] output channel: " + err.Error()})
	}
}

// parseEvent turns a line into an event when it is one.
func parseEvent(raw string) (map[string]any, bool) {
	line := strings.TrimSpace(raw)
	if !strings.HasPrefix(line, "{") {
		return nil, false
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return nil, false
	}
	if _, ok := event["ev"]; !ok {
		return nil, false
	}
	return event, true
}

// toLogLine strips everything but colours; empty lines are dropped.
func toLogLine(raw string) (LogLine, bool) {
	text := strings.TrimRight(nonColorEscape.ReplaceAllString(raw, ""), " \t")
	if strings.TrimSpace(colorEscape.ReplaceAllString(text, "")) == "" {
		return LogLine{}, false
	}
	return LogLine{Text: text}, true
}

// splitLinesAndReturns also splits at a carriage return, so a line that
// redraws itself with "\r" does not grow into one giant line.
func splitLinesAndReturns(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, c := range data {
		if c == '\n' || c == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// RequestStop triggers the same clean stop as Ctrl+C in the console: the file
// in progress is abandoned, finished files stay. A second call makes the CLI
// quit at once — its own built-in behaviour.
func (r *Runner) RequestStop() error {
	r.mu.Lock()
	pid := r.pid
	if r.cmd == nil || pid == 0 {
		r.mu.Unlock()
		return errors.New("nothing is running")
	}
	again := r.stopping
	r.stopping = true
	input := r.input
	r.input = nil
	state := r.state
	r.mu.Unlock()

	// An open question holds the CLI in a read where the signal is not seen.
	// Closing the input releases it with the safe default; the stop then
	// takes effect at the next step.
	if input != nil {
		_ = input.Close()
	}
	if err := requestCleanStop(pid); err != nil {
		return err
	}
	note := "[gui] Stop requested — finishing the current step, then stopping."
	if again {
		note = "[gui] Stop requested a second time — SuperSubtitler quits immediately."
	}
	r.sink("run:log", LogLine{Text: note})
	state.Running, state.Stopping = true, true
	r.sink("run:state", state)
	return nil
}

// Answer sends one answer line — exactly what a person would type. Only one
// answer may be on its way, and only after the question was announced.
func (r *Runner) Answer(text string) error {
	r.mu.Lock()
	input := r.input
	r.mu.Unlock()
	if input == nil {
		return errors.New("nothing is waiting for an answer")
	}
	line := strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if _, err := io.WriteString(input, line+"\n"); err != nil {
		return fmt.Errorf("runner.go: Answer: %w", err)
	}
	return nil
}
