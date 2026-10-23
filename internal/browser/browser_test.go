package browser

import (
	"os/exec"
	"testing"
	"time"
)

func TestManagerDelegatesToProcess(t *testing.T) {
	process := NewProcessManager()
	m := NewManager(process)

	if m.ProcessManager() != process {
		t.Fatal("expected ProcessManager() to return the instance passed to NewManager")
	}
	if m.IsRunning() {
		t.Fatal("expected IsRunning() to be false for a fresh process manager")
	}
}

type exitEvent struct {
	url      string
	lifetime time.Duration
}

func TestProcessManagerOnExitUnexpected(t *testing.T) {
	pm := NewProcessManager()
	pm.startCmd = func(streamURL string) *exec.Cmd {
		return exec.Command("sleep", "30")
	}

	exits := make(chan exitEvent, 1)
	pm.SetOnExit(func(url string, lifetime time.Duration) {
		exits <- exitEvent{url, lifetime}
	})

	const url = "https://twitch.tv/chan"
	if err := pm.OpenStream(url); err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if !pm.IsRunning() {
		t.Fatal("expected process to be running")
	}

	// Simulate the user closing the window: kill the process ourselves.
	pm.mu.Lock()
	cmd := pm.cmd
	pm.mu.Unlock()
	if cmd == nil {
		t.Fatal("expected cmd to be set")
	}
	_ = cmd.Process.Kill()

	select {
	case ev := <-exits:
		if ev.url != url {
			t.Fatalf("onExit url = %q, want %q", ev.url, url)
		}
		if ev.lifetime <= 0 {
			t.Fatalf("onExit lifetime = %v, want > 0", ev.lifetime)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onExit was not called for an unexpected exit")
	}
}

func TestProcessManagerNoOnExitAfterCloseStream(t *testing.T) {
	pm := NewProcessManager()
	pm.startCmd = func(streamURL string) *exec.Cmd {
		return exec.Command("sleep", "30")
	}

	exits := make(chan exitEvent, 1)
	pm.SetOnExit(func(url string, lifetime time.Duration) {
		exits <- exitEvent{url, lifetime}
	})

	if err := pm.OpenStream("https://twitch.tv/chan"); err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := pm.CloseStream(); err != nil {
		t.Fatalf("CloseStream: %v", err)
	}

	select {
	case ev := <-exits:
		t.Fatalf("onExit fired for an intentional close: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}
