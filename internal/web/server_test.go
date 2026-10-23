package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestDashboardEmbedsInitialChannels(t *testing.T) {
	s := newTestServer()
	s.monitor.SetChannels([]monitor.Channel{{Login: "foo", ID: "1"}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	s.handleDashboard(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `"login":"foo"`) {
		t.Fatal("dashboard body does not contain the embedded channel login")
	}
	if !strings.Contains(body, "initialChannels") {
		t.Fatal("dashboard body does not contain 'initialChannels'")
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

func TestNormalizeLoginsRejectsInvalid(t *testing.T) {
	for _, in := range []string{"../settings", "foo bar", "a?x=1", strings.Repeat("a", 26)} {
		if _, err := normalizeLogins([]string{in}); err == nil {
			t.Fatalf("normalizeLogins(%q) = nil error, want error", in)
		}
	}
	out, err := normalizeLogins([]string{" Foo_Bar1 "})
	if err != nil {
		t.Fatalf("normalizeLogins returned error: %v", err)
	}
	if len(out) != 1 || out[0] != "foo_bar1" {
		t.Fatalf("normalizeLogins = %v, want [foo_bar1]", out)
	}
}

func TestParseChannelLogin(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"diam0ndbrain", "diam0ndbrain", false},
		{"  Diam0ndBrain  ", "diam0ndbrain", false},
		{"https://www.twitch.tv/Diam0ndBrain", "diam0ndbrain", false},
		{"http://twitch.tv/diam0ndbrain/", "diam0ndbrain", false},
		{"twitch.tv/diam0ndbrain", "diam0ndbrain", false},
		{"https://m.twitch.tv/diam0ndbrain?ref=x", "diam0ndbrain", false},
		{"www.twitch.tv/diam0ndbrain/videos", "diam0ndbrain", false},
		{"", "", false},
		{"https://twitch.tv/", "", true},
		{"https://youtube.com/diam0ndbrain", "", true},
		{"https://twitch.tv/bad name", "", true},
		{"foo/bar", "", true},
		{"https://nottwitch.tv/diam0ndbrain", "", true},
	}
	for _, tc := range cases {
		got, err := parseChannelLogin(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseChannelLogin(%q) = %q, nil error; want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseChannelLogin(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseChannelLogin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeLoginsAcceptsURLs(t *testing.T) {
	out, err := normalizeLogins([]string{"https://www.twitch.tv/Foo", "foo", " BAR "})
	if err != nil {
		t.Fatalf("normalizeLogins returned error: %v", err)
	}
	if len(out) != 2 || out[0] != "foo" || out[1] != "bar" {
		t.Fatalf("normalizeLogins = %v, want [foo bar]", out)
	}
}

func TestHandleBrowserOpenRejectsNonTwitchURL(t *testing.T) {
	s := newTestServer()

	form := url.Values{}
	form.Set("channel", "https://youtube.com/diam0ndbrain")
	req := httptest.NewRequest(http.MethodPost, "/api/browser/open", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleBrowserOpen(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleBrowserOpenRejectsInvalidChannel(t *testing.T) {
	s := newTestServer()

	form := url.Values{}
	form.Set("channel", "settings/security")
	req := httptest.NewRequest(http.MethodPost, "/api/browser/open", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleBrowserOpen(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleChannelsRejectsInvalidLogin(t *testing.T) {
	s := newTestServer()

	body := `{"channels":["bad name"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChannels(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid channel name") {
		t.Fatalf("body = %q, want it to mention 'invalid channel name'", w.Body.String())
	}
}

type fakeBrowser struct {
	mu      sync.Mutex
	running bool
	opened  []string
	closed  int
	onExit  func(string, time.Duration)
}

func (f *fakeBrowser) OpenStream(u string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, u)
	f.running = true
	return nil
}

func (f *fakeBrowser) CloseStream() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	f.running = false
	return nil
}

func (f *fakeBrowser) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeBrowser) SetOnExit(fn func(string, time.Duration)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onExit = fn
}

func (f *fakeBrowser) openedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

func newTestServerFakeBrowser() (*Server, *fakeBrowser) {
	fb := &fakeBrowser{}
	s := NewServer("3000", browser.NewManager(browser.NewProcessManager()))
	s.browserMgr = fb
	s.reopenDelay = 50 * time.Millisecond
	return s, fb
}

// openChannel makes c1 live and reconciles so the server "opens" it.
func openChannel(s *Server) {
	s.monitor.SetChannels([]monitor.Channel{{Login: "c1", ID: "1"}})
	s.OnStreamOnline(&twitch.WSEvent{BroadcasterUserID: "1"})
}

func TestHandleBrowserExitReopens(t *testing.T) {
	s, fb := newTestServerFakeBrowser()
	openChannel(s)
	if s.monitor.Current() != "c1" {
		t.Fatal("expected c1 to be current")
	}
	if fb.openedCount() != 1 {
		t.Fatalf("opened = %v, want 1 open", fb.opened)
	}

	s.handleBrowserExit("https://twitch.tv/c1", 30*time.Second)

	if s.monitor.Current() != "" {
		t.Fatal("unexpected exit should clear current")
	}
	login, at, _, _ := s.monitor.Status()
	if login != "c1" || at.IsZero() {
		t.Fatalf("Status = (%q, %v), want pending reopen for c1", login, at)
	}

	time.Sleep(300 * time.Millisecond)
	if got := fb.openedCount(); got != 2 {
		t.Fatalf("opened count = %d, want 2 (initial + reopen)", got)
	}
	if s.monitor.Current() != "c1" {
		t.Fatalf("Current = %q, want c1 after reopen", s.monitor.Current())
	}
}

func TestHandleBrowserExitQuickExitsMarkFailed(t *testing.T) {
	s, fb := newTestServerFakeBrowser()
	openChannel(s)

	for i := 0; i < maxQuickExits; i++ {
		s.handleBrowserExit("https://twitch.tv/c1", 2*time.Second)
		if i == maxQuickExits-1 {
			break
		}
		time.Sleep(200 * time.Millisecond) // let the reopen land so current is set again
		if s.monitor.Current() != "c1" {
			t.Fatalf("iteration %d: Current = %q, want c1", i, s.monitor.Current())
		}
	}

	_, _, paused, failed := s.monitor.Status()
	if failed != "c1" || paused != "c1" {
		t.Fatalf("Status = paused %q failed %q, want c1/c1", paused, failed)
	}
	opensBefore := fb.openedCount()
	time.Sleep(200 * time.Millisecond)
	if fb.openedCount() != opensBefore {
		t.Fatal("no reopen should happen after MarkFailed")
	}
	if s.monitor.Current() != "" {
		t.Fatal("failed login should not be current")
	}
}

func TestHandleBrowserExitIgnoresOtherURL(t *testing.T) {
	s, fb := newTestServerFakeBrowser()
	openChannel(s)

	s.handleBrowserExit("https://twitch.tv/someoneelse", 2*time.Second)

	if s.monitor.Current() != "c1" {
		t.Fatal("unrelated window exit must not clear current")
	}
	if l, _, _, _ := s.monitor.Status(); l != "" {
		t.Fatalf("no reopen should be scheduled, got %q", l)
	}
	_ = fb
}

func TestHandleBrowserClosePauses(t *testing.T) {
	s, _ := newTestServerFakeBrowser()
	openChannel(s)

	req := httptest.NewRequest(http.MethodPost, "/api/browser/close", nil)
	w := httptest.NewRecorder()
	s.handleBrowserClose(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if s.monitor.Current() != "" {
		t.Fatal("close should clear current")
	}
	if _, _, p, _ := s.monitor.Status(); p != "c1" {
		t.Fatalf("paused = %q, want c1", p)
	}
	// While paused, reconcile must not reopen it.
	s.applyAction(s.monitor.Reconcile())
	if s.monitor.Current() != "" {
		t.Fatal("paused channel must not reopen")
	}
}

func TestResyncLiveStatusAPIErrorKeepsStream(t *testing.T) {
	s, _ := newTestServerFakeBrowser()
	openChannel(s)

	apiErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiErr.Close()

	client := twitch.NewClient("id", "sec", "tok", "rtok")
	client.BaseURL = apiErr.URL
	s.SetTwitchClient(client)

	s.ResyncLiveStatus()

	if s.monitor.Current() != "c1" {
		t.Fatal("a failed resync must not drop the open stream")
	}
	if !s.monitor.Channels()[0].Live {
		t.Fatal("a failed resync must not mark the channel offline")
	}
}

func TestResyncLiveStatusAppliesMap(t *testing.T) {
	s, _ := newTestServerFakeBrowser()
	s.monitor.SetChannels([]monitor.Channel{{Login: "c1", ID: "1"}, {Login: "c2", ID: "2"}})

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"s1","user_id":"2","user_login":"c2","type":"live"}]}`)
	}))
	defer api.Close()

	client := twitch.NewClient("id", "sec", "tok", "rtok")
	client.BaseURL = api.URL
	s.SetTwitchClient(client)

	s.ResyncLiveStatus()

	if s.monitor.Current() != "c2" {
		t.Fatalf("Current = %q, want c2 after resync", s.monitor.Current())
	}
}
