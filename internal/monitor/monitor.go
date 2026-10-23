// Package monitor tracks an ordered list of watched Twitch channels and decides
// which (if any) should be open in the browser based on priority and live status.
package monitor

import (
	"sync"
	"time"
)

const MaxChannels = 5

// Channel is a monitored Twitch channel in priority order.
type Channel struct {
	Login string `json:"login"`
	ID    string `json:"id"`
	Live  bool   `json:"live"`
}

// Action is what the caller must do to the browser after Reconcile.
type Action struct {
	Close bool     // close the currently open stream first
	Open  *Channel // open this channel (nil = nothing to open)
}

// Monitor holds the channel list and which login is currently open.
type Monitor struct {
	mu        sync.Mutex
	channels  []Channel
	current   string // login currently open, "" if none
	paused    string // login whose auto-open is suspended until it goes offline or the target changes
	failed    string // login whose browser keeps dying; auto-reopen stopped
	holdLogin string // login pending a delayed reopen
	holdUntil time.Time
	now       func() time.Time
}

// New creates an empty Monitor.
func New() *Monitor {
	return &Monitor{now: time.Now}
}

// SetChannels replaces the channel list. If the currently open login is no
// longer in the list, current is kept as-is; Reconcile will resolve it.
func (m *Monitor) SetChannels(chs []Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = append([]Channel(nil), chs...)
}

// SetLive sets the live flag on the channel with the given ID.
// Returns false if no channel has that ID.
func (m *Monitor) SetLive(id string, live bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.channels {
		if m.channels[i].ID == id {
			m.channels[i].Live = live
			if !live && m.channels[i].Login == m.paused {
				m.paused = ""
				m.failed = ""
			}
			return true
		}
	}
	return false
}

// SetLiveMap sets the live flag on every channel from the map (missing = false).
// Returns whether anything changed.
func (m *Monitor) SetLiveMap(live map[string]bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for i := range m.channels {
		l := live[m.channels[i].ID]
		if m.channels[i].Live != l {
			changed = true
		}
		m.channels[i].Live = l
		if !l && m.channels[i].Login == m.paused {
			m.paused = ""
			m.failed = ""
		}
	}
	return changed
}

// Channels returns a copy of the channel list.
func (m *Monitor) Channels() []Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Channel{}, m.channels...)
}

// IDs returns the channel IDs in priority order.
func (m *Monitor) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, len(m.channels))
	for i, c := range m.channels {
		ids[i] = c.ID
	}
	return ids
}

// Current returns the login currently open, or "".
func (m *Monitor) Current() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Pause suspends auto-opening the given login until it goes offline or the
// target channel changes. Clears current if it equals login.
func (m *Monitor) Pause(login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = login
	if m.holdLogin == login {
		m.holdLogin = ""
		m.holdUntil = time.Time{}
	}
	if m.current == login {
		m.current = ""
	}
}

// HoldReopen schedules a delayed reopen for the given login, clearing current
// if it equals login.
func (m *Monitor) HoldReopen(login string, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holdLogin = login
	m.holdUntil = until
	if m.current == login {
		m.current = ""
	}
}

// MarkFailed marks the given login as failed (browser keeps dying): it is
// paused and any pending reopen hold is cleared.
func (m *Monitor) MarkFailed(login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = login
	m.paused = login
	m.holdLogin = ""
	m.holdUntil = time.Time{}
	if m.current == login {
		m.current = ""
	}
}

// ClearCurrent clears current if it equals login.
func (m *Monitor) ClearCurrent(login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == login {
		m.current = ""
	}
}

// Status reports pending-reopen, paused, and failed state for the API.
func (m *Monitor) Status() (reopenLogin string, reopenAt time.Time, paused string, failed string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holdLogin, m.holdUntil, m.paused, m.failed
}

// Desired returns a copy of the first live channel, or nil if none are live.
func (m *Monitor) Desired() *Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.desired()
}

// desired must be called with m.mu held.
func (m *Monitor) desired() *Channel {
	for i := range m.channels {
		if m.channels[i].Live {
			c := m.channels[i]
			return &c
		}
	}
	return nil
}

// Reconcile compares the desired channel with the currently open one and
// returns the browser action needed to converge.
func (m *Monitor) Reconcile() Action {
	m.mu.Lock()
	defer m.mu.Unlock()

	d := m.desired()

	// Drop stale hold/pause state when the target changed or nothing is live.
	if d == nil || d.Login != m.holdLogin {
		m.holdLogin = ""
		m.holdUntil = time.Time{}
	}
	if d == nil || d.Login != m.paused {
		m.paused = ""
		m.failed = ""
	}

	if d == nil {
		if m.current != "" {
			m.current = ""
			return Action{Close: true}
		}
		return Action{}
	}

	// A hold that has expired clears and proceeds normally.
	held := d.Login == m.holdLogin
	if held && !m.now().Before(m.holdUntil) {
		m.holdLogin = ""
		m.holdUntil = time.Time{}
		held = false
	}

	if d.Login == m.paused || held {
		if m.current != "" {
			m.current = ""
			return Action{Close: true}
		}
		return Action{}
	}

	if d.Login == m.current {
		return Action{}
	}

	a := Action{Close: m.current != "", Open: d}
	m.current = d.Login
	return a
}
