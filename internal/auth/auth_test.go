package auth

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := Settings{GameName: "Rust", DropsActive: true}
	if err := SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	got, err := GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got != want {
		t.Fatalf("GetSettings = %+v, want %+v", got, want)
	}
}

func TestClearCredentialsKeepsSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := SaveCredentials("id", "sec", "rt"); err != nil {
		t.Fatalf("SaveCredentials failed: %v", err)
	}
	if err := SaveMonitoredChannels([]string{"shroud"}); err != nil {
		t.Fatalf("SaveMonitoredChannels failed: %v", err)
	}
	want := Settings{GameName: "Rust", DropsActive: true}
	if err := SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	if err := ClearCredentials(); err != nil {
		t.Fatalf("ClearCredentials failed: %v", err)
	}

	cid, csec, rtoken, err := GetCredentials()
	if err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if cid != "" || csec != "" || rtoken != "" {
		t.Fatalf("GetCredentials = (%q, %q, %q), want all empty", cid, csec, rtoken)
	}

	chs, err := GetMonitoredChannels()
	if err != nil {
		t.Fatalf("GetMonitoredChannels failed: %v", err)
	}
	if len(chs) != 0 {
		t.Fatalf("GetMonitoredChannels = %v, want empty", chs)
	}

	got, err := GetSettings()
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if got != want {
		t.Fatalf("GetSettings = %+v, want %+v", got, want)
	}
}

func TestSaveCredentialsPartialUpdate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := SaveCredentials("id", "sec", "rt"); err != nil {
		t.Fatalf("SaveCredentials failed: %v", err)
	}
	if err := SaveCredentials("", "", "rt2"); err != nil {
		t.Fatalf("SaveCredentials failed: %v", err)
	}

	cid, csec, rtoken, err := GetCredentials()
	if err != nil {
		t.Fatalf("GetCredentials failed: %v", err)
	}
	if cid != "id" || csec != "sec" || rtoken != "rt2" {
		t.Fatalf("GetCredentials = (%q, %q, %q), want (id, sec, rt2)", cid, csec, rtoken)
	}
}

func TestMonitoredChannelsMigratesLegacy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := getConfigFile()
	if err != nil {
		t.Fatalf("getConfigFile failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"monitored_channel":"shroud"}`), 0600); err != nil {
		t.Fatalf("failed to write legacy config: %v", err)
	}

	chs, err := GetMonitoredChannels()
	if err != nil {
		t.Fatalf("GetMonitoredChannels failed: %v", err)
	}
	if len(chs) != 1 || chs[0] != "shroud" {
		t.Fatalf("GetMonitoredChannels = %v, want [shroud]", chs)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to re-read config: %v", err)
	}
	var cfg StoredConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}
	if len(cfg.MonitoredChannels) != 1 || cfg.MonitoredChannels[0] != "shroud" {
		t.Fatalf("monitored_channels = %v, want [shroud]", cfg.MonitoredChannels)
	}
	if cfg.MonitoredChannel != "" {
		t.Fatalf("legacy monitored_channel = %q, want empty", cfg.MonitoredChannel)
	}
}
