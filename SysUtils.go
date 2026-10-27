//go:build windows && amd64

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// errProcessStall is the cancel cause set when the streaming stall-watchdog kills
// a child that produced no output for streamStallLimit.
var errProcessStall = errors.New("process stall timeout")

// streamStallLimit is the maximum gap between two engine output lines before the
// child is treated as hung (e.g. a wedged GPU driver). Generous on purpose: real
// transcription emits @PROGRESS far more often, even on the CPU fallback.
const streamStallLimit = 10 * time.Minute

// ----------------------------------------------------------------------------
// Win32 constants
// ----------------------------------------------------------------------------

const (
	winCREATE_NO_WINDOW                   = 0x08000000
	winCREATE_NEW_PROCESS_GROUP           = 0x00000200
	winIDLE_PRIORITY_CLASS                = 0x00000040
	winENABLE_VIRTUAL_TERMINAL_PROCESSING = 0x0004

	// Job object
	winJobObjectExtendedLimitInformation  = 9
	winJOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000

	// OpenProcess access rights needed to assign a child to a job
	winPROCESS_SET_QUOTA     = 0x0100
	winPROCESS_TERMINATE     = 0x0001
	winPROCESS_ASSIGN_TO_JOB = winPROCESS_SET_QUOTA | winPROCESS_TERMINATE
)

// ----------------------------------------------------------------------------
// Win32 procedures
// ----------------------------------------------------------------------------

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetConsMode             = modKernel32.NewProc("GetConsoleMode")
	procSetConsMode             = modKernel32.NewProc("SetConsoleMode")
	procSetConsoleCtrlHandler   = modKernel32.NewProc("SetConsoleCtrlHandler")
	procGetLongPathNameW        = modKernel32.NewProc("GetLongPathNameW")
	procCreateFile              = modKernel32.NewProc("CreateFileW")
	procSetFileTime             = modKernel32.NewProc("SetFileTime")
	procCloseHandle             = modKernel32.NewProc("CloseHandle")
	procOpenProcess             = modKernel32.NewProc("OpenProcess")
	procCreateJobObject         = modKernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob      = modKernel32.NewProc("AssignProcessToJobObject")
)

// ----------------------------------------------------------------------------
// Job object structs
//
// Field order mirrors the Win32 headers 1:1. The explicit _padN fields sit
// exactly where MSVC inserts alignment padding, so Go reproduces the same
// 64-bit layout (BasicLimit = 64 bytes, Extended = 144 bytes) and go vet is
// happy. Do NOT reorder.
// ----------------------------------------------------------------------------

type jobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	_pad0                   uint32 // align MinimumWorkingSetSize to 8
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	_pad1                   uint32 // align Affinity to 8
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIoCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimitInformation struct {
	BasicLimitInformation jobBasicLimitInformation
	IoInfo                jobIoCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobHandle is created once in initJobObject and never written again. Holding
// it open for the whole process lifetime is what arms KILL_ON_JOB_CLOSE: when
// this process dies for any reason (including a Task-Manager kill), the OS
// releases the handle, the job closes, and every assigned child is terminated.
var jobHandle uintptr

// ----------------------------------------------------------------------------
// ANSI console
// ----------------------------------------------------------------------------

func enableAnsiConsole() {
	h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}
	var mode uint32
	ret, _, _ := procGetConsMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if ret == 0 {
		return
	}
	procSetConsMode.Call(uintptr(h), uintptr(mode|winENABLE_VIRTUAL_TERMINAL_PROCESSING))
}

// ----------------------------------------------------------------------------
// Ctrl handler: window close / logoff / shutdown
// ----------------------------------------------------------------------------

// setupConsoleCtrlHandler cancels the root context on a window-close, logoff or
// shutdown event and gives running children a brief moment to wind down. The
// hard guarantee against orphans is the job object, not this handler.
func setupConsoleCtrlHandler(cancel func()) {
	cb := syscall.NewCallback(func(ctrlType uint32) uintptr {
		const (
			CTRL_CLOSE_EVENT    = 2
			CTRL_LOGOFF_EVENT   = 5
			CTRL_SHUTDOWN_EVENT = 6
		)
		if ctrlType != CTRL_CLOSE_EVENT &&
			ctrlType != CTRL_LOGOFF_EVENT &&
			ctrlType != CTRL_SHUTDOWN_EVENT {
			return 0
		}
		cancel()
		time.Sleep(2 * time.Second)
		return 1
	})
	procSetConsoleCtrlHandler.Call(cb, 1)
}

// ----------------------------------------------------------------------------
// Job object
// ----------------------------------------------------------------------------

// initJobObject creates an anonymous job with KILL_ON_JOB_CLOSE. Every child
// launched via runProcess is assigned to it, so no audio converter or whisper
// engine can survive the parent process.
func initJobObject() error {
	h, _, callErr := procCreateJobObject.Call(0, 0)
	if h == 0 {
		return fmt.Errorf("SysUtils.go: initJobObject (CreateJobObject): %w", callErr)
	}
	info := jobExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = winJOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ret, _, callErr := procSetInformationJobObject.Call(
		h,
		uintptr(winJobObjectExtendedLimitInformation),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		procCloseHandle.Call(h)
		return fmt.Errorf("SysUtils.go: initJobObject (SetInformationJobObject): %w", callErr)
	}
	jobHandle = h
	return nil
}

// assignToJob attaches a started child process (by PID) to the job. A failure
// here is non-fatal for the run itself, but it weakens the orphan guarantee, so
// the caller logs it.
func assignToJob(pid int) error {
	if jobHandle == 0 || pid <= 0 {
		return nil
	}
	h, _, callErr := procOpenProcess.Call(winPROCESS_ASSIGN_TO_JOB, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("SysUtils.go: assignToJob (OpenProcess pid %d): %w", pid, callErr)
	}
	defer procCloseHandle.Call(h)
	ret, _, callErr := procAssignProcessToJob.Call(jobHandle, h)
	if ret == 0 {
		return fmt.Errorf("SysUtils.go: assignToJob (AssignProcessToJobObject pid %d): %w", pid, callErr)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Central process launcher
// ----------------------------------------------------------------------------

// runProcess runs an external command to completion, hidden, idle-priority and
// assigned to the job object. It returns captured stdout. On a non-zero exit the
// error carries the tail of stderr so failures are never silent.
func runProcess(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winCREATE_NO_WINDOW | winCREATE_NEW_PROCESS_GROUP | winIDLE_PRIORITY_CLASS,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SysUtils.go: runProcess (start %s): %w", baseName(name), err)
	}
	if cmd.Process != nil {
		if jerr := assignToJob(cmd.Process.Pid); jerr != nil {
			pWarn.Printf("Job assignment failed for %s: %v\n", baseName(name), jerr)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return stdout.Bytes(), ctx.Err()
		}
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 600 {
			tail = "..." + tail[len(tail)-600:]
		}
		return stdout.Bytes(), fmt.Errorf("SysUtils.go: runProcess (%s exited): %w | %s",
			baseName(name), err, tail)
	}
	return stdout.Bytes(), nil
}

// runProcessStreaming behaves like runProcess but streams stdout line by line to
// onLine while the process is still running (used for live progress). stderr is
// buffered and appended to the error on failure, exactly like runProcess.
func runProcessStreaming(ctx context.Context, onLine func(string), name string, args ...string) error {
	return runProcessStreamingStall(ctx, streamStallLimit, onLine, name, args...)
}

// runProcessStreamingStall is runProcessStreaming with an explicit stall limit;
// 0 disables the watchdog (pip and model downloads, which can be silent for a
// long time without being hung).
func runProcessStreamingStall(ctx context.Context, stallLimit time.Duration, onLine func(string), name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// runCtx lets the stall-watchdog kill a hung child independently of the parent
	// context, with a distinguishable cause (errProcessStall).
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winCREATE_NO_WINDOW | winCREATE_NEW_PROCESS_GROUP | winIDLE_PRIORITY_CLASS,
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("SysUtils.go: runProcessStreaming (stdout pipe): %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SysUtils.go: runProcessStreaming (start %s): %w", baseName(name), err)
	}
	if cmd.Process != nil {
		if jerr := assignToJob(cmd.Process.Pid); jerr != nil {
			pWarn.Printf("Job assignment failed for %s: %v\n", baseName(name), jerr)
		}
	}

	// Stall-watchdog: armed only after the FIRST output line, so the (possibly
	// minutes-long) one-time model download that happens before transcription
	// begins never trips it. Each subsequent line resets it; a gap longer than
	// streamStallLimit means the engine is hung and the child is killed.
	var watchdog *time.Timer
	sc := bufio.NewScanner(stdoutPipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if stallLimit > 0 {
			if watchdog == nil {
				watchdog = time.AfterFunc(stallLimit, func() { cancel(errProcessStall) })
				defer watchdog.Stop()
			} else {
				watchdog.Reset(stallLimit)
			}
		}
		if onLine != nil {
			onLine(sc.Text())
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(context.Cause(runCtx), errProcessStall) {
			return fmt.Errorf("SysUtils.go: runProcessStreaming (%s stalled: no output for %s)",
				baseName(name), stallLimit)
		}
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 600 {
			tail = "..." + tail[len(tail)-600:]
		}
		return fmt.Errorf("SysUtils.go: runProcessStreaming (%s exited): %w | %s",
			baseName(name), err, tail)
	}
	return nil
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ----------------------------------------------------------------------------
// Long paths
// ----------------------------------------------------------------------------

func getLongPathName(path string) string {
	const prefix = `\\?\`
	const uncPrefix = `\\?\UNC\`

	queryPath := path
	isUNC := strings.HasPrefix(path, `\\`) && !strings.HasPrefix(path, `\\?\`)
	if isUNC {
		queryPath = uncPrefix + strings.TrimPrefix(path, `\\`)
	} else if !strings.HasPrefix(queryPath, prefix) {
		queryPath = prefix + queryPath
	}
	ptr, err := syscall.UTF16PtrFromString(queryPath)
	if err != nil {
		return path
	}
	n, _, _ := procGetLongPathNameW.Call(uintptr(unsafe.Pointer(ptr)), 0, 0)
	if n == 0 {
		return path
	}
	buf := make([]uint16, n)
	ret, _, _ := procGetLongPathNameW.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&buf[0])),
		n,
	)
	if ret == 0 {
		return path
	}
	resultPath := syscall.UTF16ToString(buf)
	if strings.HasPrefix(resultPath, uncPrefix) {
		return `\\` + strings.TrimPrefix(resultPath, uncPrefix)
	}
	return strings.TrimPrefix(resultPath, prefix)
}

// ----------------------------------------------------------------------------
// Timestamp preservation
// ----------------------------------------------------------------------------

// copyTimestamps copies creation/access/write times from src to dest so the
// generated MKV inherits the original file's dates.
func copyTimestamps(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("SysUtils.go: copyTimestamps (stat src): %w", err)
	}
	sysInfo, ok := srcInfo.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return nil
	}
	ctime := sysInfo.CreationTime
	atime := sysInfo.LastAccessTime
	mtime := sysInfo.LastWriteTime

	destPath := dest
	if len(destPath) > 240 && !strings.HasPrefix(destPath, `\\?\`) {
		if strings.HasPrefix(destPath, `\\`) {
			destPath = `\\?\UNC\` + strings.TrimPrefix(destPath, `\\`)
		} else {
			destPath = `\\?\` + destPath
		}
	}
	destPtr, err := syscall.UTF16PtrFromString(destPath)
	if err != nil {
		return fmt.Errorf("SysUtils.go: copyTimestamps (UTF16PtrFromString): %w", err)
	}
	const (
		FILE_WRITE_ATTRIBUTES     = 0x00000100
		FILE_SHARE_READ_WRITE_DEL = 0x00000007
		OPEN_EXISTING             = 3
		FILE_ATTRIBUTE_NORMAL     = 0x00000080
	)
	h, _, e := procCreateFile.Call(
		uintptr(unsafe.Pointer(destPtr)),
		FILE_WRITE_ATTRIBUTES,
		FILE_SHARE_READ_WRITE_DEL,
		0, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, 0,
	)
	if h == uintptr(syscall.InvalidHandle) {
		return fmt.Errorf("SysUtils.go: copyTimestamps (CreateFile): %w", e)
	}
	defer procCloseHandle.Call(h)

	ret, _, e := procSetFileTime.Call(
		h,
		uintptr(unsafe.Pointer(&ctime)),
		uintptr(unsafe.Pointer(&atime)),
		uintptr(unsafe.Pointer(&mtime)),
	)
	if ret == 0 {
		return fmt.Errorf("SysUtils.go: copyTimestamps (SetFileTime): %w", e)
	}
	return nil
}
