//go:build windows

// app.go — everything the window can ask of Go. Each exported method of App
// is callable from index.html (window.go.main.App.*).
package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"SuperSubtitlerGUI/core"

	"github.com/wailsapp/wails/v2/pkg/options"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// App is bound to the window.
type App struct {
	ctx    context.Context
	runner *Runner

	window           windowState
	windowRemembered bool

	// The batches still to run after the current one (very long file lists
	// are split so each command line stays below the Windows limit).
	mu      sync.Mutex
	flags   []string
	pending [][]string
}

func NewApp(window windowState, remembered bool) *App {
	a := &App{window: window, windowRemembered: remembered}
	a.runner = NewRunner(a.emit, a.onRunExit)
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.windowRemembered && !a.window.Maximised &&
		rectIsOnAScreen(a.window.X, a.window.Y, a.window.Width, a.window.Height) {
		wailsruntime.WindowSetPosition(ctx, a.window.X, a.window.Y)
	}
}

func (a *App) emit(name string, data ...any) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, name, data...)
	}
}

func (a *App) note(text string) { a.emit("run:log", LogLine{Text: "[gui] " + text}) }

// beforeClose refuses to close while a run is active: closing would cut the
// CLI off mid-file. Otherwise the window size and place are remembered.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.runner.Running() {
		_, _ = wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{
			Type:    wailsruntime.WarningDialog,
			Title:   "SuperSubtitler is still working",
			Message: "A run is still in progress. Press Stop first, then close the window.",
		})
		return true
	}
	a.rememberWindow()
	return false
}

func (a *App) rememberWindow() {
	s, ok := loadWindowState()
	if !ok {
		s = defaultWindowState()
		s.Theme = a.window.Theme
	}
	s.Maximised = wailsruntime.WindowIsMaximised(a.ctx)
	if !s.Maximised && !wailsruntime.WindowIsMinimised(a.ctx) {
		s.Width, s.Height = wailsruntime.WindowGetSize(a.ctx)
		s.X, s.Y = wailsruntime.WindowGetPosition(a.ctx)
	}
	_ = saveWindowState(s)
}

func (a *App) onSecondInstance(options.SecondInstanceData) {
	if a.ctx == nil {
		return
	}
	wailsruntime.WindowUnminimise(a.ctx)
	wailsruntime.WindowShow(a.ctx)
	a.note("SuperSubtitler is already open — this is that window.")
}

// ----------------------------------------------------------------------------
// Start-up, theme, CLI
// ----------------------------------------------------------------------------

// StartupInfo is everything the window needs before it draws itself.
type StartupInfo struct {
	GuiVersion string    `json:"guiVersion"`
	Theme      string    `json:"theme"`
	CLI        CLIStatus `json:"cli"`
}

func (a *App) GetStartupInfo() StartupInfo {
	return StartupInfo{GuiVersion: guiVersion, Theme: a.window.Theme, CLI: probeCLI()}
}

func (a *App) GetCLIStatus() CLIStatus { return probeCLI() }

func (a *App) DownloadCLI() (CLIStatus, error) {
	if a.runner.Running() {
		return CLIStatus{}, errors.New("not while a run is in progress")
	}
	return downloadCLI()
}

// RestoreCLIBackup puts SuperSubtitler.exe.bak back in place.
func (a *App) RestoreCLIBackup() (CLIStatus, error) {
	if a.runner.Running() {
		return probeCLI(), errors.New("not while a run is in progress")
	}
	return restoreCLIBackup()
}

func (a *App) SaveTheme(theme string) error {
	a.window.Theme = normaliseTheme(theme)
	s, ok := loadWindowState()
	if !ok {
		s = defaultWindowState()
	}
	s.Theme = a.window.Theme
	return saveWindowState(s)
}

// ----------------------------------------------------------------------------
// Files
// ----------------------------------------------------------------------------

// AddResult is what was taken from a drop or a file dialog — and what not.
type AddResult struct {
	Items    []core.QueueItem `json:"items"`
	Rejected []core.Rejected  `json:"rejected"`
}

func acceptFor(page string) func(core.Kind) bool {
	if page == "merge" {
		return core.AcceptMergeInput
	}
	return core.AcceptSubtitleInput
}

func (a *App) AddPaths(paths []string, page string) AddResult {
	items, rejected := core.ExpandPaths(paths, acceptFor(page))
	return AddResult{Items: items, Rejected: rejected}
}

func (a *App) PickFiles(page string) (AddResult, error) {
	filters := []wailsruntime.FileFilter{{
		DisplayName: "Video and audio",
		Pattern:     "*.mkv;*.mp4;*.mov;*.m4v;*.avi;*.ts;*.mts;*.m2ts;*.webm;*.mpg;*.mpeg;*.wmv;*.flv;*.vob;*.3gp;*.m4a;*.mp3;*.wav;*.flac;*.aac;*.ogg;*.opus;*.m4b",
	}}
	if page == "merge" {
		filters = append(filters, wailsruntime.FileFilter{DisplayName: "Subtitles", Pattern: "*.srt;*.ass;*.ssa;*.vtt;*.sup;*.idx;*.sub"})
	}
	paths, err := wailsruntime.OpenMultipleFilesDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: "Add files", Filters: filters})
	if err != nil || len(paths) == 0 {
		return AddResult{}, err
	}
	return a.AddPaths(paths, page), nil
}

func (a *App) PickFolder(page string) (AddResult, error) {
	dir, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: "Add a folder (subfolders included)"})
	if err != nil || dir == "" {
		return AddResult{}, err
	}
	return a.AddPaths([]string{dir}, page), nil
}

func (a *App) PickOutputFolder() (string, error) {
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: "Write the results into…"})
}

// OpenFolder shows a file in Explorer (selected), or opens a folder.
func (a *App) OpenFolder(path string) error {
	if path == "" {
		path = guiDir()
	}
	args := []string{path}
	if fileExists(path) {
		args = []string{"/select,", path}
	}
	cmd := exec.Command("explorer.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.Start() // explorer's exit code means nothing; do not wait
}

// ----------------------------------------------------------------------------
// Runs
// ----------------------------------------------------------------------------

func (a *App) readyCLI() error {
	if a.runner.Running() {
		return errors.New("a run is already in progress")
	}
	st := probeCLI()
	switch {
	case !st.Found:
		return errors.New(cliFileName + " was not found next to this window — use \"Get SuperSubtitler\" on the About page")
	case st.TooOld:
		return errors.New(cliFileName + " " + st.Version + " is too old for this window (needs " + core.MinCLIVersion + " or newer)")
	case st.Problem != "":
		return errors.New(cliFileName + ": " + st.Problem)
	}
	return nil
}

func (a *App) StartSubtitle(opts core.RunOptions, files []string) error {
	if len(files) == 0 {
		return errors.New("add some files first")
	}
	if err := a.readyCLI(); err != nil {
		return err
	}
	flags, err := core.SubtitleArgs(opts)
	if err != nil {
		return err
	}
	batches := core.Batches(flags, files, core.MaxCommandLine)
	a.mu.Lock()
	a.flags, a.pending = flags, batches[1:]
	a.mu.Unlock()
	return a.runner.Start(cliPath(), guiDir(), append(append([]string{}, flags...), batches[0]...),
		RunState{Kind: "subtitle", Batch: 1, Batches: len(batches)})
}

// StartSplit exports every channel of the chosen track as a WAV (-split).
func (a *App) StartSplit(track int, files []string) error {
	if len(files) == 0 {
		return errors.New("add some files first")
	}
	if err := a.readyCLI(); err != nil {
		return err
	}
	flags, err := core.SplitArgs(track)
	if err != nil {
		return err
	}
	batches := core.Batches(flags, files, core.MaxCommandLine)
	a.mu.Lock()
	a.flags, a.pending = flags, batches[1:]
	a.mu.Unlock()
	return a.runner.Start(cliPath(), guiDir(), append(append([]string{}, flags...), batches[0]...),
		RunState{Kind: "split", Batch: 1, Batches: len(batches)})
}

func (a *App) StartMerge(outDir, video string, extras []string) error {
	if err := a.readyCLI(); err != nil {
		return err
	}
	args, err := core.MergeArgs(outDir, video, extras)
	if err != nil {
		return err
	}
	a.clearPending()
	return a.runner.Start(cliPath(), guiDir(), args, RunState{Kind: "merge", Batch: 1, Batches: 1})
}

func (a *App) StartSetup(translate bool) error {
	if err := a.readyCLI(); err != nil {
		return err
	}
	a.clearPending()
	return a.runner.Start(cliPath(), guiDir(), core.SetupArgs(translate), RunState{Kind: "setup", Batch: 1, Batches: 1})
}

func (a *App) clearPending() {
	a.mu.Lock()
	a.flags, a.pending = nil, nil
	a.mu.Unlock()
}

// onRunExit starts the next batch, or reports the end. A stop by the user
// (exit code 2 or a requested stop) ends the whole batch list.
func (a *App) onRunExit(state RunState) {
	a.mu.Lock()
	more := len(a.pending) > 0 && !state.Stopping && state.ExitCode != 2 && state.Error == ""
	var next []string
	if more {
		next = append(append([]string{}, a.flags...), a.pending[0]...)
		a.pending = a.pending[1:]
	} else {
		a.pending = nil
	}
	a.mu.Unlock()

	if more {
		// The finished process must be fully gone before the next starts.
		time.Sleep(200 * time.Millisecond)
		err := a.runner.Start(cliPath(), guiDir(), next,
			RunState{Kind: state.Kind, Batch: state.Batch + 1, Batches: state.Batches})
		if err == nil {
			return
		}
		state.Error = err.Error()
	}
	a.emit("run:state", state)
}

func (a *App) Answer(text string) error { return a.runner.Answer(text) }

func (a *App) Stop() error {
	a.clearPending()
	return a.runner.RequestStop()
}

func (a *App) IsRunning() bool { return a.runner.Running() }

// ----------------------------------------------------------------------------
// Taskbar
// ----------------------------------------------------------------------------

func (a *App) ShowProgress(percent int) {
	wailsruntime.WindowSetTitle(a.ctx, progressTitle(percent))
	showTaskbarProgress(percent)
}

func (a *App) ShowBusy() {
	wailsruntime.WindowSetTitle(a.ctx, busyTitle())
	showTaskbarBusy()
}

func (a *App) HideProgress() {
	wailsruntime.WindowSetTitle(a.ctx, baseWindowTitle())
	hideTaskbarProgress()
}

func (a *App) SignalDone() { flashTaskbar() }

// ----------------------------------------------------------------------------
// Settings and SRT cleaner
// ----------------------------------------------------------------------------

// SettingsView is SuperSubtitler_Config.ini, made readable.
type SettingsView struct {
	Path     string            `json:"path"`
	Exists   bool              `json:"exists"`
	Sections []core.IniSection `json:"sections"`
	Problem  string            `json:"problem"`
}

// ensureCLIFiles lets the CLI write its two settings files with their
// documented defaults when they do not exist yet: a run without files and with
// -nowait creates them, shows its help and exits (stdin stays closed).
func ensureCLIFiles() {
	if fileExists(filepath.Join(guiDir(), cliConfigName)) && fileExists(filepath.Join(guiDir(), cliCleanerName)) {
		return
	}
	if !fileExists(cliPath()) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPath(), "-nowait")
	cmd.Dir = guiDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	_ = cmd.Run()
}

func (a *App) GetSettings() SettingsView {
	ensureCLIFiles()
	v := SettingsView{Path: filepath.Join(guiDir(), cliConfigName)}
	raw, err := os.ReadFile(v.Path)
	if err != nil {
		v.Problem = "The settings file does not exist yet. It is created the first time SuperSubtitler runs."
		return v
	}
	v.Exists = true
	v.Sections = core.ParseIni(string(raw))
	return v
}

// SaveSettings writes changed values in place. The CLI checks the result
// first: a value it would reject is refused here, instead of making the next
// start reset the whole file. The previous file is kept as .bak.
func (a *App) SaveSettings(values map[string]string) (SettingsView, error) {
	path := filepath.Join(guiDir(), cliConfigName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return a.GetSettings(), err
	}
	updated, err := core.ApplyIniValues(string(raw), values)
	if err != nil {
		return a.GetSettings(), err
	}
	if err := checkConfigText(updated); err != nil {
		return a.GetSettings(), errors.New("not saved — " + err.Error())
	}
	_ = os.WriteFile(path+".bak", raw, 0o644)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return a.GetSettings(), err
	}
	return a.GetSettings(), nil
}

// CleanerView is the SRT cleaner's term list as plain text.
type CleanerView struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Text   string `json:"text"`
}

func (a *App) GetCleaner() CleanerView {
	ensureCLIFiles()
	v := CleanerView{Path: filepath.Join(guiDir(), cliCleanerName)}
	if raw, err := os.ReadFile(v.Path); err == nil {
		v.Exists, v.Text = true, string(raw)
	}
	return v
}

// ResetSettings restores SuperSubtitler_Config.ini to the CLI's documented
// defaults: the current file is kept as .bak, and the CLI writes a fresh one
// (it creates the file whenever it is missing). The window itself never
// writes default values, so they can never drift from the CLI's.
func (a *App) ResetSettings() (SettingsView, error) {
	if err := a.restoreDefaultFile(cliConfigName); err != nil {
		return a.GetSettings(), err
	}
	return a.GetSettings(), nil
}

// ResetCleaner does the same for the SRT cleaner's term list.
func (a *App) ResetCleaner() (CleanerView, error) {
	if err := a.restoreDefaultFile(cliCleanerName); err != nil {
		return a.GetCleaner(), err
	}
	return a.GetCleaner(), nil
}

func (a *App) restoreDefaultFile(name string) error {
	if a.runner.Running() {
		return errors.New("not while a run is in progress — it reads this file")
	}
	if !fileExists(cliPath()) {
		return errors.New(cliFileName + " is needed to write the defaults, and it was not found")
	}
	path := filepath.Join(guiDir(), name)
	if fileExists(path) {
		_ = os.Remove(path + ".bak")
		if err := os.Rename(path, path+".bak"); err != nil {
			return errors.New("could not keep the current file as .bak: " + err.Error())
		}
	}
	ensureCLIFiles()
	if !fileExists(path) {
		// Put the old file back rather than leave the user with none.
		_ = os.Rename(path+".bak", path)
		return errors.New(cliFileName + " did not write the default file")
	}
	return nil
}

func (a *App) SaveCleaner(text string) error {
	path := filepath.Join(guiDir(), cliCleanerName)
	if old, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", old, 0o644)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return os.WriteFile(path, []byte(strings.ReplaceAll(text, "\n", "\r\n")), 0o644)
}
