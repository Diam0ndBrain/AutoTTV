package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ProcessManager implements StreamManager by spawning a browser subprocess.
type ProcessManager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	url       string
	startedAt time.Time
	closing   *exec.Cmd
	onExit    func(url string, lifetime time.Duration)

	// startCmd, when set, replaces browser detection/args (used by tests).
	startCmd func(streamURL string) *exec.Cmd
}

// NewProcessManager creates a new ProcessManager instance.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{}
}

// SetOnExit registers a callback invoked (in a new goroutine) when the browser
// process exits without CloseStream having been requested.
func (m *ProcessManager) SetOnExit(fn func(url string, lifetime time.Duration)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onExit = fn
}

// OpenStream spawns a new, isolated browser window navigating to the stream URL.
func (m *ProcessManager) OpenStream(streamURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		// Process is already running, just try to bring it to foreground
		go bringToForeground(m.cmd.Process.Pid)
		return nil
	}

	var cmd *exec.Cmd
	browserType := "custom"
	if m.startCmd != nil {
		cmd = m.startCmd(streamURL)
	} else {
		browserPath, bt, err := findBrowser()
		if err != nil {
			return fmt.Errorf("failed to find a supported browser: %w", err)
		}
		browserType = bt

		// Create a persistent profile path in the user's home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get user home directory: %w", err)
		}
		profileDir := filepath.Join(homeDir, ".autottv", "browser_profile")
		if err := os.MkdirAll(profileDir, 0755); err != nil {
			return fmt.Errorf("failed to create browser profile directory: %w", err)
		}

		var args []string
		switch browserType {
		case "chrome", "edge":
			args = []string{
				fmt.Sprintf("--user-data-dir=%s", profileDir),
				"--new-window",
				"--window-position=0,0",
				"--window-size=1280,720",
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-backgrounding-occluded-windows",
				"--disable-background-timer-throttling",
				"--disable-renderer-backgrounding",
				"--disable-features=CalculatePageVisibility",
				streamURL,
			}
		case "firefox":
			args = []string{
				"--profile", profileDir,
				"--new-window",
				streamURL,
			}
		default:
			// Fallback to basic open
			args = []string{streamURL}
		}
		cmd = exec.Command(browserPath, args...)

		// Ensure the process starts in a new process group
		setProcessGroup(cmd)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start browser process: %w", err)
	}

	m.cmd = cmd
	m.url = streamURL
	m.startedAt = time.Now()
	fmt.Printf("Spawned browser [%s] with PID %d to watch: %s\n", browserType, cmd.Process.Pid, streamURL)

	// Monitor the process state in background to cleanly update m.cmd when it exits
	go func(url string, startedAt time.Time) {
		_ = cmd.Wait()
		m.mu.Lock()
		unexpected := m.closing != cmd
		if m.closing == cmd {
			m.closing = nil
		}
		if m.cmd == cmd {
			m.cmd = nil
			m.url = ""
			m.startedAt = time.Time{}
		}
		fn := m.onExit
		m.mu.Unlock()
		// Never run the callback while holding m.mu: applyAction holds
		// actionMu while CloseStream polls m.mu, so a synchronous call could deadlock.
		if unexpected && fn != nil {
			go fn(url, time.Since(startedAt))
		}
	}(streamURL, m.startedAt)

	// Attempt to force the window into the foreground / active state
	go bringToForeground(cmd.Process.Pid)

	return nil
}

func (m *ProcessManager) CloseStream() error {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return nil
	}

	pid := m.cmd.Process.Pid
	cmd := m.cmd
	m.closing = cmd // mark as an intentional close so the Wait goroutine skips onExit
	m.mu.Unlock()   // Unlock to allow the background Wait() goroutine to acquire the lock and clear m.cmd

	fmt.Printf("Gracefully closing browser process with PID %d...\n", pid)

	// Attempt graceful termination
	if runtime.GOOS == "windows" {
		gracefulCmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid))
		_ = gracefulCmd.Run()
	} else {
		_ = cmd.Process.Signal(os.Interrupt)
	}

	// Poll up to 5 seconds for the background goroutine to detect exit and clear m.cmd
	for range 50 {
		m.mu.Lock()
		if m.cmd == nil {
			m.mu.Unlock()
			fmt.Println("Browser exited cleanly.")
			return nil
		}
		m.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	// Timeout, force kill
	fmt.Printf("Browser did not exit within 5s. Force-killing PID %d...\n", pid)
	if runtime.GOOS == "windows" {
		forceKillCmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
		_ = forceKillCmd.Run()
	} else {
		_ = cmd.Process.Kill()
	}

	// Poll up to 2 seconds for force kill to complete and update state
	for range 20 {
		m.mu.Lock()
		if m.cmd == nil {
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	// Force set to nil if still stuck
	m.mu.Lock()
	m.cmd = nil
	m.url = ""
	m.startedAt = time.Time{}
	if m.closing == cmd {
		m.closing = nil
	}
	m.mu.Unlock()

	return nil
}

// IsRunning returns true if the spawned browser process is actively running.
func (m *ProcessManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// findBrowser searches for Chrome, Edge, or Firefox in common system locations.
func findBrowser() (string, string, error) {
	if runtime.GOOS == "windows" {
		paths := []struct {
			path string
			name string
		}{
			{`C:\Program Files\Google\Chrome\Application\chrome.exe`, "chrome"},
			{`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`, "chrome"},
			{`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, "edge"},
			{`C:\Program Files\Microsoft\Edge\Application\msedge.exe`, "edge"},
			{`C:\Program Files\Mozilla Firefox\firefox.exe`, "firefox"},
		}

		for _, bp := range paths {
			if _, err := os.Stat(bp.path); err == nil {
				return bp.path, bp.name, nil
			}
		}

		// Fallback: check registry or PATH
		if path, err := exec.LookPath("chrome"); err == nil {
			return path, "chrome", nil
		}
		if path, err := exec.LookPath("msedge"); err == nil {
			return path, "edge", nil
		}
		if path, err := exec.LookPath("firefox"); err == nil {
			return path, "firefox", nil
		}
	} else if runtime.GOOS == "darwin" {
		paths := []struct {
			path string
			name string
		}{
			{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "chrome"},
			{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", "edge"},
			{"/Applications/Firefox.app/Contents/MacOS/firefox", "firefox"},
		}

		for _, bp := range paths {
			if _, err := os.Stat(bp.path); err == nil {
				return bp.path, bp.name, nil
			}
		}
	} else { // Linux
		paths := []struct {
			path string
			name string
		}{
			{"/usr/bin/google-chrome", "chrome"},
			{"/usr/bin/microsoft-edge", "edge"},
			{"/usr/bin/firefox", "firefox"},
		}

		for _, bp := range paths {
			if _, err := os.Stat(bp.path); err == nil {
				return bp.path, bp.name, nil
			}
		}
	}

	return "", "", errors.New("no supported browser found in default directories")
}

var _ StreamManager = (*ProcessManager)(nil)
