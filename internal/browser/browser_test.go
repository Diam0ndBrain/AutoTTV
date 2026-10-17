package browser

import "testing"

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
