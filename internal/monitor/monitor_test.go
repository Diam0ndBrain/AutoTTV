package monitor

import "testing"

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
