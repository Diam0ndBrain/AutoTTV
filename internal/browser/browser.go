package browser

import "time"

// StreamManager defines the interface for open, close and status checks of a stream player.
type StreamManager interface {
	OpenStream(streamURL string) error
	CloseStream() error
	IsRunning() bool
}

// Manager wraps the ProcessManager so existing *browser.Manager call sites keep working.
type Manager struct {
	process *ProcessManager
}

// NewManager creates a new Manager that delegates to the given ProcessManager.
func NewManager(process *ProcessManager) *Manager {
	return &Manager{
		process: process,
	}
}

// ProcessManager returns the stored ProcessManager instance.
func (m *Manager) ProcessManager() *ProcessManager {
	return m.process
}

// OpenStream opens the stream window via the process manager.
func (m *Manager) OpenStream(streamURL string) error {
	return m.process.OpenStream(streamURL)
}

// CloseStream closes the stream window via the process manager.
func (m *Manager) CloseStream() error {
	return m.process.CloseStream()
}

// IsRunning returns whether the stream window is running.
func (m *Manager) IsRunning() bool {
	return m.process.IsRunning()
}

// SetOnExit registers a callback for unexpected browser process exits.
func (m *Manager) SetOnExit(fn func(url string, lifetime time.Duration)) {
	m.process.SetOnExit(fn)
}
