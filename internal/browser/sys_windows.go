//go:build windows

package browser

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
)

const (
	SW_RESTORE     = 9
	SWP_NOSIZE     = 0x0001
	SWP_NOMOVE     = 0x0002
	SWP_SHOWWINDOW = 0x0040
)

var (
	HWND_TOPMOST   = ^uintptr(0)     // -1
	HWND_NOTOPMOST = ^uintptr(0) - 1 // -2
)

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// CREATE_NEW_PROCESS_GROUP = 0x00000200
	cmd.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP
}

func bringToForeground(pid int) {
	// Let's retry a few times as the window might take a moment to spawn
	for attempt := 0; attempt < 10; attempt++ {
		var foundHwnd uintptr

		cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
			var windowPid uint32
			procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPid)))

			if int(windowPid) == pid {
				visible, _, _ := procIsWindowVisible.Call(hwnd)
				if visible != 0 {
					foundHwnd = hwnd
					return 0 // Stop enumeration
				}
			}
			return 1 // Continue enumeration
		})

		procEnumWindows.Call(cb, 0)

		if foundHwnd != 0 {
			// 1. Restore the window from any minimized state
			procShowWindow.Call(foundHwnd, SW_RESTORE)

			// 2. Force it to the very top of the Z-order (Topmost Toggle)
			procSetWindowPos.Call(foundHwnd, HWND_TOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_SHOWWINDOW)

			// 3. Attach input threads and set foreground to bypass focus restrictions
			fgHwnd, _, _ := procGetForegroundWindow.Call()
			var fgPid uint32
			fgThreadID, _, _ := procGetWindowThreadProcessId.Call(fgHwnd, uintptr(unsafe.Pointer(&fgPid)))
			currentThreadID, _, _ := procGetCurrentThreadId.Call()

			if fgThreadID != 0 && currentThreadID != 0 && fgThreadID != currentThreadID {
				procAttachThreadInput.Call(currentThreadID, fgThreadID, 1)
				procSetForegroundWindow.Call(foundHwnd)
				procAttachThreadInput.Call(currentThreadID, fgThreadID, 0)
			} else {
				procSetForegroundWindow.Call(foundHwnd)
			}

			// 4. Revert it to a normal window (so it doesn't stay permanently stuck on top)
			procSetWindowPos.Call(foundHwnd, HWND_NOTOPMOST, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_SHOWWINDOW)
			return
		}

		time.Sleep(200 * time.Millisecond)
	}
}
