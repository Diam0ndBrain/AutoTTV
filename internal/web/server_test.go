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

func doRoutes(s *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	return w
}

func TestDashboardDoesNotLeakSecret(t *testing.T) {
	s := newTestServer()
	s.clientId = "myid"
	s.clientSecret = "supersecretvalue"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, "supersecretvalue") {
		t.Fatal("dashboard body contains the client secret")
	}
	if !strings.Contains(body, "myid") {
		t.Fatal("dashboard body does not contain the client ID")
	}
	if !strings.Contains(body, "Reconnect with Twitch") {
		t.Fatal("dashboard body does not contain 'Reconnect with Twitch'")
	}
}

func TestDashboardEscapesGameName(t *testing.T) {
	s := newTestServer()
	s.gameName = `"><script>alert(1)</script>`

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("dashboard body contains unescaped game name")
	}
	if !strings.Contains(body, "&lt;script&gt;") && !strings.Contains(body, `\x3cscript`) && !strings.Contains(body, "&#60;script&#62;") {
		t.Fatal("dashboard body does not contain an escaped form of the game name")
	}
}

func TestDashboardRemoteHidesSetupForm(t *testing.T) {
	s := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:1234"
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, `name="client_secret"`) {
		t.Fatal("remote dashboard contains the client_secret input")
	}
	if !strings.Contains(body, "configured from the host machine") {
		t.Fatal("remote dashboard does not show the host-machine notice")
	}
}

func TestDashboardRemoteAuthenticatedHidesLogout(t *testing.T) {
	s := newTestServer()
	s.isAuthenticated = true

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:1234"
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, `action="/api/logout"`) {
		t.Fatal("remote dashboard contains a logout form")
	}
	if !strings.Contains(body, "only available from the host machine") {
		t.Fatal("remote authenticated dashboard does not show the host-machine notice")
	}
}

func TestHandleSetupReusesStoredSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s := newTestServer()
	s.clientId = "id"
	s.clientSecret = "sec"

	form := url.Values{}
	form.Set("client_id", "id")
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleSetup(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if s.clientSecret != "sec" {
		t.Fatalf("clientSecret = %q, want stored secret preserved", s.clientSecret)
	}
	if s.oauthState == "" {
		t.Fatal("oauthState was not stored")
	}
}

func TestHandleSetupRejectsBlankSecret(t *testing.T) {
	t.Run("nothing stored", func(t *testing.T) {
		s := newTestServer()
		form := url.Values{}
		form.Set("client_id", "id")
		req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleSetup(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("different client id", func(t *testing.T) {
		s := newTestServer()
		s.clientId = "id"
		s.clientSecret = "sec"
		form := url.Values{}
		form.Set("client_id", "other")
		req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleSetup(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}

func TestLoopbackOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newTestServer()

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Host = "localhost:3000"
	req.RemoteAddr = "192.168.1.50:1234"
	if w := doRoutes(s, req); w.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, want 403", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Host = "localhost:3000"
	req.RemoteAddr = "127.0.0.1:1234"
	if w := doRoutes(s, req); w.Code == http.StatusForbidden {
		t.Fatal("loopback request was forbidden")
	}
}

func TestHostCheck(t *testing.T) {
	s := newTestServer()

	for _, tc := range []struct {
		host string
		want int
	}{
		{"evil.example", http.StatusForbidden},
		{"192.168.1.20:3000", http.StatusOK},
		{"localhost:3000", http.StatusOK},
		{"[::1]:3000", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = tc.host
		req.RemoteAddr = "127.0.0.1:1234"
		if w := doRoutes(s, req); w.Code != tc.want {
			t.Fatalf("Host %q: status = %d, want %d", tc.host, w.Code, tc.want)
		}
	}
}

func TestSameOriginOnly(t *testing.T) {
	s := newTestServer()
	body := `{"channels":["a"]}`

	for _, tc := range []struct {
		name    string
		origin  string
		fetch   string
		want403 bool
	}{
		{"evil origin", "https://evil.example", "", true},
		{"matching origin", "http://192.168.1.20:3000", "", false},
		{"no origin", "", "", false},
		{"cross-site fetch", "", "cross-site", true},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body))
		req.Host = "192.168.1.20:3000"
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.fetch != "" {
			req.Header.Set("Sec-Fetch-Site", tc.fetch)
		}
		w := doRoutes(s, req)
		if tc.want403 && w.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", tc.name, w.Code)
		}
		if !tc.want403 && w.Code == http.StatusForbidden {
			t.Fatalf("%s: status = 403, want non-403", tc.name)
		}
	}
}
