package monitor

import (
	"testing"
	"time"
)

func ch(login, id string, live bool) Channel {
	return Channel{Login: login, ID: id, Live: live}
}

func assertAction(t *testing.T, a Action, wantClose bool, wantOpen string) {
	t.Helper()
	if a.Close != wantClose {
		t.Fatalf("Close = %v, want %v", a.Close, wantClose)
	}
	if wantOpen == "" {
		if a.Open != nil {
			t.Fatalf("Open = %+v, want nil", a.Open)
		}
	} else {
		if a.Open == nil || a.Open.Login != wantOpen {
			t.Fatalf("Open = %+v, want login %q", a.Open, wantOpen)
		}
	}
}

func TestMonitorReconcileScenarios(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", false), ch("c2", "2", false), ch("c3", "3", false)})

	// 1. c2 goes live -> open c2, no close
	m.SetLive("2", true)
	assertAction(t, m.Reconcile(), false, "c2")
	if m.Current() != "c2" {
		t.Fatalf("Current = %q, want c2", m.Current())
	}

	// 2. c3 goes live -> c2 has priority, no action
	m.SetLive("3", true)
	assertAction(t, m.Reconcile(), false, "")

	// 3. c1 goes live -> close c2, open c1
	m.SetLive("1", true)
	assertAction(t, m.Reconcile(), true, "c1")

	// 4. c1 goes offline -> close c1, open c2 (next live)
	m.SetLive("1", false)
	assertAction(t, m.Reconcile(), true, "c2")

	// 5. c2 goes offline (c3 still live) -> close c2, open c3
	m.SetLive("2", false)
	assertAction(t, m.Reconcile(), true, "c3")

	// 6. all offline -> close only; second reconcile -> nothing
	m.SetLive("3", false)
	assertAction(t, m.Reconcile(), true, "")
	assertAction(t, m.Reconcile(), false, "")
}

func TestMonitorSetChannelsRemovesCurrent(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", true)})
	assertAction(t, m.Reconcile(), false, "c1")

	// c1 removed, c2 live -> close + open c2
	m.SetChannels([]Channel{ch("c2", "2", true), ch("c3", "3", false)})
	assertAction(t, m.Reconcile(), true, "c2")

	// c1 removed, none live -> close only
	m2 := New()
	m2.SetChannels([]Channel{ch("c1", "1", true)})
	assertAction(t, m2.Reconcile(), false, "c1")
	m2.SetChannels([]Channel{ch("c2", "2", false), ch("c3", "3", false)})
	assertAction(t, m2.Reconcile(), true, "")
}

func TestMonitorSetLiveUnknownID(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", false)})
	if m.SetLive("nope", true) {
		t.Fatal("SetLive with unknown ID should return false")
	}
	assertAction(t, m.Reconcile(), false, "")
}

func TestMonitorReorder(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", true), ch("c2", "2", true)})
	assertAction(t, m.Reconcile(), false, "c1")

	m.SetChannels([]Channel{ch("c2", "2", true), ch("c1", "1", true)})
	assertAction(t, m.Reconcile(), true, "c2")
}

func TestMonitorIDsAndChannelsCopy(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", false), ch("c2", "2", true)})

	ids := m.IDs()
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "2" {
		t.Fatalf("IDs = %v", ids)
	}

	chs := m.Channels()
	chs[0].Live = true // mutate the copy
	if m.Channels()[0].Live {
		t.Fatal("Channels() must return a copy")
	}

	if d := m.Desired(); d == nil || d.Login != "c2" {
		t.Fatalf("Desired = %+v, want c2", d)
	}
}

func TestMonitorHoldReopen(t *testing.T) {
	m := New()
	now := time.Now()
	m.now = func() time.Time { return now }

	m.SetChannels([]Channel{ch("c1", "1", true)})
	assertAction(t, m.Reconcile(), false, "c1")

	// Window died: hold c1 for 60s -> nothing to open until it expires
	m.HoldReopen("c1", now.Add(60*time.Second))
	if m.Current() != "" {
		t.Fatal("HoldReopen should clear current")
	}
	assertAction(t, m.Reconcile(), false, "")

	login, at, _, _ := m.Status()
	if login != "c1" || !at.Equal(now.Add(60*time.Second)) {
		t.Fatalf("Status = (%q, %v), want reopen for c1", login, at)
	}

	now = now.Add(61 * time.Second)
	assertAction(t, m.Reconcile(), false, "c1")
	if l, _, _, _ := m.Status(); l != "" {
		t.Fatalf("hold should be cleared after expiry, got %q", l)
	}
}

func TestMonitorHoldClearsOnTargetChange(t *testing.T) {
	m := New()
	now := time.Now()
	m.now = func() time.Time { return now }

	m.SetChannels([]Channel{ch("c1", "1", false), ch("c2", "2", true)})
	assertAction(t, m.Reconcile(), false, "c2")

	m.HoldReopen("c2", now.Add(60*time.Second))
	assertAction(t, m.Reconcile(), false, "")

	// Higher-priority c1 goes live -> hold on c2 is dropped, c1 opens
	m.SetLive("1", true)
	assertAction(t, m.Reconcile(), false, "c1")
	if l, _, _, _ := m.Status(); l != "" {
		t.Fatalf("hold should be cleared on target change, got %q", l)
	}
}

func TestMonitorPause(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", true), ch("c2", "2", false)})
	assertAction(t, m.Reconcile(), false, "c1")

	m.Pause("c1")
	if m.Current() != "" {
		t.Fatal("Pause should clear current")
	}
	assertAction(t, m.Reconcile(), false, "")
	if _, _, p, _ := m.Status(); p != "c1" {
		t.Fatalf("Status paused = %q, want c1", p)
	}

	// Higher-priority channel goes live -> pause on c1 dropped, c0 opens
	m2 := New()
	m2.SetChannels([]Channel{ch("c0", "0", false), ch("c1", "1", true)})
	assertAction(t, m2.Reconcile(), false, "c1")
	m2.Pause("c1")
	m2.SetLive("0", true)
	assertAction(t, m2.Reconcile(), false, "c0")
	if _, _, p, _ := m2.Status(); p != "" {
		t.Fatalf("pause should clear on target change, got %q", p)
	}

	// Channel goes offline -> pause clears; coming back live reopens it
	m.SetLive("1", false)
	assertAction(t, m.Reconcile(), false, "")
	if _, _, p, _ := m.Status(); p != "" {
		t.Fatalf("pause should clear when channel goes offline, got %q", p)
	}
	m.SetLive("1", true)
	assertAction(t, m.Reconcile(), false, "c1")
}

func TestMonitorMarkFailed(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", true)})
	assertAction(t, m.Reconcile(), false, "c1")

	m.HoldReopen("c1", time.Now().Add(time.Hour))
	m.MarkFailed("c1")
	if m.Current() != "" {
		t.Fatal("MarkFailed should clear current")
	}
	l, _, p, f := m.Status()
	if l != "" {
		t.Fatalf("MarkFailed should clear the hold, got %q", l)
	}
	if p != "c1" || f != "c1" {
		t.Fatalf("Status = paused %q failed %q, want c1/c1", p, f)
	}

	// Behaves like pause: nothing opens while it stays live
	assertAction(t, m.Reconcile(), false, "")

	// Going offline clears the failure; online again reopens
	m.SetLive("1", false)
	if _, _, p, f := m.Status(); p != "" || f != "" {
		t.Fatalf("offline should clear pause/failed, got %q/%q", p, f)
	}
	m.SetLive("1", true)
	assertAction(t, m.Reconcile(), false, "c1")
}

func TestMonitorClearCurrentAndHoldKeepOtherCurrent(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", true), ch("c2", "2", false)})
	assertAction(t, m.Reconcile(), false, "c1")

	m.ClearCurrent("c2")
	if m.Current() != "c1" {
		t.Fatal("ClearCurrent for another login must not clear current")
	}
	m.HoldReopen("c2", time.Now().Add(time.Hour))
	if m.Current() != "c1" {
		t.Fatal("HoldReopen for another login must not clear current")
	}
	// The stray hold is dropped on the next reconcile and c1 stays open.
	assertAction(t, m.Reconcile(), false, "")

	m.ClearCurrent("c1")
	if m.Current() != "" {
		t.Fatal("ClearCurrent should clear matching login")
	}
}

func TestMonitorSetLiveMap(t *testing.T) {
	m := New()
	m.SetChannels([]Channel{ch("c1", "1", false), ch("c2", "2", true)})

	if !m.SetLiveMap(map[string]bool{"1": true}) {
		t.Fatal("SetLiveMap should report a change")
	}
	if !m.Channels()[0].Live || m.Channels()[1].Live {
		t.Fatal("SetLiveMap did not apply the map (missing = false)")
	}
	if m.SetLiveMap(map[string]bool{"1": true}) {
		t.Fatal("SetLiveMap should report no change for identical map")
	}

	// Pause clears when the paused channel goes offline via the map
	assertAction(t, m.Reconcile(), false, "c1")
	m.Pause("c1")
	m.SetLiveMap(map[string]bool{"1": false})
	if _, _, p, _ := m.Status(); p != "" {
		t.Fatalf("SetLiveMap offline should clear pause, got %q", p)
	}
}
