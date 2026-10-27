//go:build windows

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// taskbar.go — progress on the taskbar button, the percentage in the window
// title, and a flash when a batch is done. Adapted from NVENCForgeGUI
// (https://github.com/burnersen/NVENCForgeGUI, GPL-3.0-only, same author).
//
// ITaskbarList3 is a COM interface and must be used from one OS thread, so
// every command goes through one goroutine that is locked to its thread.
package main

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Die Aufträge, die der COM-Faden kennt.
const (
	taskbarSetProgress = iota // Balken auf einen Prozentwert setzen
	taskbarSetBusy            // Es läuft etwas, aber ohne Prozentwert (laufendes Muster)
	taskbarSetIdle            // Balken wegnehmen
	taskbarStartFlash         // Knopf blinken lassen
)

// Zustände des Taskleisten-Balkens (ITaskbarList3::SetProgressState).
const (
	tbpfNoProgress    = 0x0
	tbpfIndeterminate = 0x1
	tbpfNormal        = 0x2
)

// FlashWindowEx: Knopf UND Titelleiste blinken, und zwar so lange, bis das
// Fenster nach vorn geholt wird. Genau das war der Wunsch — eine Meldung, die
// man nicht verpasst, aber auch nicht wegklicken muss.
const (
	flashAll       = 0x00000003
	flashTimerNoFG = 0x0000000C
)

// GetWindow-Abfrage "wer besitzt dieses Fenster?". Ein Werkzeugfenster hat
// einen Besitzer, das Hauptfenster nicht.
const gwOwner = 4

// CoCreateInstance: der Server läuft im eigenen Prozess.
const clsctxInprocServer = 0x1

var (
	ole32                        = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance         = ole32.NewProc("CoCreateInstance")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindow                = user32.NewProc("GetWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procFlashWindowEx            = user32.NewProc("FlashWindowEx")
)

// Die beiden Kennungen des Taskleisten-Dienstes (shobjidl.h).
var (
	clsidTaskbarList = windows.GUID{
		Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0,
		Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90},
	}
	iidTaskbarList3 = windows.GUID{
		Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86,
		Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF},
	}
)

// taskbarCommand ist ein Auftrag an den COM-Faden.
type taskbarCommand struct {
	kind    int
	percent int
}

var (
	taskbarOnce  sync.Once
	taskbarQueue chan taskbarCommand
)

// showTaskbarProgress setzt den Balken auf einen Prozentwert (0–100).
func showTaskbarProgress(percent int) {
	sendTaskbarCommand(taskbarCommand{kind: taskbarSetProgress, percent: clampPercent(percent)})
}

// showTaskbarBusy zeigt "es läuft", wenn es keinen ehrlichen Prozentwert gibt
// — beim Zusammenfügen etwa entsteht EINE Datei, da gibt es nichts zu zählen.
func showTaskbarBusy() {
	sendTaskbarCommand(taskbarCommand{kind: taskbarSetBusy})
}

// hideTaskbarProgress nimmt den Balken wieder weg.
func hideTaskbarProgress() {
	sendTaskbarCommand(taskbarCommand{kind: taskbarSetIdle})
}

// flashTaskbar lässt den Knopf blinken — die Fertig-Meldung ohne Ton.
func flashTaskbar() {
	sendTaskbarCommand(taskbarCommand{kind: taskbarStartFlash})
}

// clampPercent hält den Wert im erlaubten Bereich.
//
// Der Wert kommt aus der Fensterseite und wird dort aus Dateigrößen gerechnet.
// Eine 101 wäre kein Weltuntergang, aber Windows zeichnet daraus einen Balken,
// der über sein eigenes Ende hinausläuft.
func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// baseWindowTitle ist der Titel, solange nichts läuft.
func baseWindowTitle() string { return "SuperSubtitler v" + guiVersion }

// busyTitle sagt, dass etwas läuft, ohne eine Zahl zu erfinden.
func busyTitle() string { return baseWindowTitle() + " — working" }

// progressTitle stellt die Prozentzahl VORAN.
//
// Der Taskleisten-Knopf zeigt nur den Anfang des Titels; steht die Zahl
// hinten, ist sie genau dann abgeschnitten, wenn man sie braucht.
func progressTitle(percent int) string {
	return fmt.Sprintf("%d %%  ·  %s", clampPercent(percent), baseWindowTitle())
}

// sendTaskbarCommand reicht einen Auftrag weiter, ohne je zu blockieren.
//
// Ist der Kanal voll, wird der Auftrag verworfen: Der nächste Fortschritts-
// wert kommt in Sekundenbruchteilen ohnehin, und die Oberfläche stehen zu
// lassen, nur damit ein Balken hübsch aussieht, wäre der falsche Tausch.
func sendTaskbarCommand(command taskbarCommand) {
	taskbarOnce.Do(func() {
		taskbarQueue = make(chan taskbarCommand, 16)
		go serveTaskbar(taskbarQueue)
	})
	select {
	case taskbarQueue <- command:
	default:
	}
}

// serveTaskbar ist der eine Faden, der mit COM redet.
func serveTaskbar(queue <-chan taskbarCommand) {
	// COM-Objekte gehören dem Thread, der sie erzeugt hat. Ohne diese Bindung
	// könnte die Go-Laufzeit den Faden auf einen anderen Thread schieben — und
	// die Aufrufe gingen ins Leere.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	list := newTaskbarList()
	if list != nil {
		defer list.release()
	}

	for command := range queue {
		window := mainWindowHandle()
		if window == 0 {
			continue
		}
		switch command.kind {
		case taskbarSetProgress:
			if list != nil {
				list.setProgressState(window, tbpfNormal)
				list.setProgressValue(window, uint64(command.percent), 100)
			}
		case taskbarSetBusy:
			if list != nil {
				list.setProgressState(window, tbpfIndeterminate)
			}
		case taskbarSetIdle:
			if list != nil {
				list.setProgressState(window, tbpfNoProgress)
			}
		case taskbarStartFlash:
			flashWindow(window)
		}
	}
}

// ----------------------------------------------------------------------------
// ITaskbarList3 — von Hand, ohne fremde Bibliothek
// ----------------------------------------------------------------------------

// taskbarListVtbl ist die Sprungtabelle der Schnittstelle. Die Reihenfolge ist
// festgelegt (IUnknown, dann ITaskbarList, ITaskbarList2, ITaskbarList3) und
// darf nicht umsortiert werden — jeder Eintrag ist eine Position, kein Name.
type taskbarListVtbl struct {
	queryInterface       uintptr
	addRef               uintptr
	release              uintptr
	hrInit               uintptr
	addTab               uintptr
	deleteTab            uintptr
	activateTab          uintptr
	setActiveAlt         uintptr
	markFullscreenWindow uintptr
	setProgressValue     uintptr
	setProgressState     uintptr
}

// taskbarList ist der Zeiger auf das COM-Objekt. Das erste Feld eines
// COM-Objekts ist immer der Zeiger auf seine Sprungtabelle.
type taskbarList struct {
	vtbl *taskbarListVtbl
}

// newTaskbarList holt den Taskleisten-Dienst. Liefert nil, wenn er nicht zu
// haben ist — dann bleibt der Knopf eben schmucklos.
func newTaskbarList() *taskbarList {
	// S_FALSE ("war schon initialisiert") und RPC_E_CHANGED_MODE ("dieser
	// Thread ist schon in einem anderen Modell") sind kein Grund aufzugeben:
	// Beides heißt, dass COM auf diesem Faden benutzbar ist.
	_ = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)

	var created *taskbarList
	result, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTaskbarList)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidTaskbarList3)),
		uintptr(unsafe.Pointer(&created)),
	)
	if result != 0 || created == nil {
		return nil
	}
	// HrInit MUSS laufen, bevor irgendetwas anderes aufgerufen wird.
	if hr, _, _ := syscall.SyscallN(created.vtbl.hrInit, uintptr(unsafe.Pointer(created))); hr != 0 {
		created.release()
		return nil
	}
	return created
}

func (t *taskbarList) release() {
	_, _, _ = syscall.SyscallN(t.vtbl.release, uintptr(unsafe.Pointer(t)))
}

func (t *taskbarList) setProgressValue(window windows.HWND, done, total uint64) {
	_, _, _ = syscall.SyscallN(t.vtbl.setProgressValue,
		uintptr(unsafe.Pointer(t)), uintptr(window), uintptr(done), uintptr(total))
}

func (t *taskbarList) setProgressState(window windows.HWND, state uint32) {
	_, _, _ = syscall.SyscallN(t.vtbl.setProgressState,
		uintptr(unsafe.Pointer(t)), uintptr(window), uintptr(state))
}

// ----------------------------------------------------------------------------
// Fensterhandle und Blinken
// ----------------------------------------------------------------------------

// flashInfo ist die FLASHWINFO-Struktur aus der Windows-API.
type flashInfo struct {
	size    uint32
	window  windows.HWND
	flags   uint32
	count   uint32
	timeout uint32
}

// flashWindow lässt den Taskleisten-Knopf blinken — aber nur, wenn das Fenster
// gerade NICHT im Vordergrund steht. Wer ohnehin hinsieht, braucht kein
// Zappeln vor der Nase.
func flashWindow(window windows.HWND) {
	front, _, _ := procGetForegroundWindow.Call()
	if windows.HWND(front) == window {
		return
	}
	info := flashInfo{
		window: window,
		flags:  flashAll | flashTimerNoFG,
	}
	info.size = uint32(unsafe.Sizeof(info))
	_, _, _ = procFlashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}

// foundWindow nimmt das Ergebnis der Suche auf. Die Suche läuft ausschließlich
// auf dem COM-Faden, deshalb braucht diese Stelle keine Sperre.
var foundWindow windows.HWND

// windowSearch wird EINMAL erzeugt: windows.NewCallback belegt dauerhaft einen
// Platz in einer begrenzten Tabelle. In einer Schleife erzeugt, wäre das ein
// Leck, das irgendwann das Programm anhält.
var windowSearch = windows.NewCallback(func(window windows.HWND, _ uintptr) uintptr {
	const keepLooking, stop = 1, 0

	var processID uint32
	_, _, _ = procGetWindowThreadProcessID.Call(uintptr(window), uintptr(unsafe.Pointer(&processID)))
	if processID != windows.GetCurrentProcessId() {
		return keepLooking
	}
	if visible, _, _ := procIsWindowVisible.Call(uintptr(window)); visible == 0 {
		return keepLooking
	}
	// Ein Fenster mit Besitzer ist ein Dialog oder Werkzeugfenster, nie das
	// Hauptfenster.
	if ownerWindow, _, _ := procGetWindow.Call(uintptr(window), gwOwner); ownerWindow != 0 {
		return keepLooking
	}
	// Die versteckte Konsole ausdrücklich ausschließen. Bei "wails dev" ist sie
	// sichtbar und käme sonst als Hauptfenster durch.
	if console, _, _ := procGetConsoleWindow.Call(); console != 0 && windows.HWND(console) == window {
		return keepLooking
	}
	foundWindow = window
	return stop
})

// cachedWindow merkt sich das gefundene Handle. Das Hauptfenster wird genau
// einmal erzeugt und lebt, solange das Programm lebt.
var cachedWindow windows.HWND

// mainWindowHandle sucht das Fenster dieses Programms. 0 heißt: noch nicht da
// (die ersten Aufrufe können vor dem Fenster liegen) — dann passiert nichts.
func mainWindowHandle() windows.HWND {
	if cachedWindow != 0 {
		return cachedWindow
	}
	foundWindow = 0
	// Der Fehler von EnumWindows wird bewusst nicht gemeldet: Bricht die
	// Aufzählung ab, weil der Rückruf "gefunden" gesagt hat, meldet Windows
	// genau das als Fehler.
	_ = windows.EnumWindows(windowSearch, nil)
	cachedWindow = foundWindow
	return cachedWindow
}
