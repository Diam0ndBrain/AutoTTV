// Package monitor tracks an ordered list of watched Twitch channels and decides
// which (if any) should be open in the browser based on priority and live status.
package monitor

import "sync"

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
	mu       sync.Mutex
	channels []Channel
	current  string // login currently open, "" if none
}

// New creates an empty Monitor.
func New() *Monitor {
	return &Monitor{}
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
			return true
		}
	}
	return false
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

	if d == nil {
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
