package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"autoTtv/internal/browser"
	"autoTtv/internal/monitor"
	"autoTtv/internal/twitch"
)

func newTestServer() *Server {
	return NewServer("3000", browser.NewManager(browser.NewProcessManager()))
}

func TestHandleCallbackRejectsBadState(t *testing.T) {
	t.Run("wrong state", func(t *testing.T) {
		s := newTestServer()
		s.oauthState = "expected"

		req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=wrong", nil)
		w := httptest.NewRecorder()
		s.handleCallback(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "state mismatch") {
			t.Fatalf("body = %q, want it to contain 'state mismatch'", w.Body.String())
		}
	})

	t.Run("no state set", func(t *testing.T) {
		s := newTestServer()

		req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=whatever", nil)
		w := httptest.NewRecorder()
		s.handleCallback(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "state mismatch") {
			t.Fatalf("body = %q, want it to contain 'state mismatch'", w.Body.String())
		}
	})
}

func TestHandleSetupStoresState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s := newTestServer()

	form := url.Values{}
	form.Set("client_id", "testid")
	form.Set("client_secret", "testsecret")
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSetup(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if s.oauthState == "" {
		t.Fatal("oauthState was not stored")
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "state="+s.oauthState) {
		t.Fatalf("Location = %q, want it to contain state=%s", loc, s.oauthState)
	}
}

func TestHandleChannelsRejectsTooMany(t *testing.T) {
	s := newTestServer()

	body := `{"channels":["a","b","c","d","e","f"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChannels(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "maximum") {
		t.Fatalf("body = %q, want it to mention the maximum", w.Body.String())
	}
}

func TestHandleChannelsRequiresAuth(t *testing.T) {
	s := newTestServer()

	body := `{"channels":["a","b"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChannels(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestOnStreamEventsReconcile(t *testing.T) {
	// Skip if a real browser could actually be launched on this machine
	for _, p := range []string{"/usr/bin/google-chrome", "/usr/bin/firefox"} {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("found real browser at %s; skipping to avoid launching a window", p)
		}
	}

	s := newTestServer()
	s.monitor.SetChannels([]monitor.Channel{
		{Login: "c1", ID: "1"},
		{Login: "c2", ID: "2"},
		{Login: "c3", ID: "3"},
	})

	// c2 goes live -> becomes current (OpenStream will fail to find a browser, only logs)
	s.OnStreamOnline(&twitch.WSEvent{BroadcasterUserID: "2"})
	if got := s.monitor.Current(); got != "c2" {
		t.Fatalf("Current = %q, want c2", got)
	}

	// c1 goes live (higher priority) -> becomes current
	s.OnStreamOnline(&twitch.WSEvent{BroadcasterUserID: "1"})
	if got := s.monitor.Current(); got != "c1" {
		t.Fatalf("Current = %q, want c1", got)
	}

	// c1 goes offline -> falls back to c2
	s.OnStreamOffline(&twitch.WSEvent{BroadcasterUserID: "1"})
	if got := s.monitor.Current(); got != "c2" {
		t.Fatalf("Current = %q, want c2", got)
	}

	// unknown channel -> no-op
	s.OnStreamOnline(&twitch.WSEvent{BroadcasterUserID: "999"})
	if got := s.monitor.Current(); got != "c2" {
		t.Fatalf("Current = %q, want c2 unchanged", got)
	}
}
