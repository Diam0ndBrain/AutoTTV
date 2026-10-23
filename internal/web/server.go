package web

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"autoTtv/internal/auth"
	"autoTtv/internal/browser"
	"autoTtv/internal/drops"
	"autoTtv/internal/monitor"
	"autoTtv/internal/twitch"
)

const (
	quickExitThreshold = 10 * time.Second // an exit faster than this after launch counts as a quick exit
	maxQuickExits      = 3                // consecutive quick exits before giving up on the login
	defaultReopenDelay = 60 * time.Second // delay before reopening a stream whose window died
)

// browserController is the subset of browser.Manager the Server needs.
type browserController interface {
	OpenStream(streamURL string) error
	CloseStream() error
	IsRunning() bool
	SetOnExit(fn func(url string, lifetime time.Duration))
}

// Server coordinates the Web UI dashboard, OAuth flow, and application state.
type Server struct {
	mu           sync.Mutex
	port         string
	browserMgr   browserController
	wsMgr        *twitch.WebSocketManager
	twitchClient *twitch.Client
	monitor      *monitor.Monitor
	actionMu     sync.Mutex // serializes browser close/open sequences from applyAction

	reopenDelay    time.Duration // delay before reopening after an unexpected window exit
	quickExits     int           // consecutive fast exits for quickExitLogin
	quickExitLogin string

	// State
	clientId        string
	clientSecret    string
	accessToken     string
	isAuthenticated bool
	oauthState      string

	// Drops Watcher State
	gameName     string
	dropsActive  bool
	campaigns    []drops.Campaign
	autoCampaign string
	watchStart   time.Time
	watchTicker  *time.Ticker
	watchDone    chan struct{}
	lastScrape   time.Time
	scrapeTicker *time.Ticker
	scrapeDone   chan struct{}

	// Channels for coordinating updates
	channelUpdateChan chan struct{}
	logoutChan        chan struct{}
}

// NewServer initializes the Web UI server.
func NewServer(port string, browserMgr *browser.Manager) *Server {
	s := &Server{
		port:              port,
		browserMgr:        browserMgr,
		monitor:           monitor.New(),
		reopenDelay:       defaultReopenDelay,
		channelUpdateChan: make(chan struct{}, 5),
		logoutChan:        make(chan struct{}, 1),
	}
	browserMgr.SetOnExit(s.handleBrowserExit)
	return s
}

// Start runs the HTTP server and opens the dashboard/setup page.
func (s *Server) Start() error {
	// Load existing credentials if they exist
	cid, csec, rtoken, _ := auth.GetCredentials()
	if cid != "" && csec != "" {
		s.clientId = cid
		s.clientSecret = csec
		if rtoken != "" {
			// Try to refresh access token immediately
			tok, _, err := auth.RefreshAccessToken(cid, csec, rtoken)
			if err == nil {
				s.accessToken = tok
				s.isAuthenticated = true
				s.twitchClient = twitch.NewClient(cid, csec, tok, rtoken)
				fmt.Println("Existing Twitch credentials loaded and refreshed successfully.")

				// Load stored channels if any
				logins, _ := auth.GetMonitoredChannels()
				if len(logins) > 0 {
					if _, err := s.loadChannels(s.twitchClient, logins); err != nil {
						fmt.Printf("Failed to restore monitored channels: %v\n", err)
					}
				}
			} else {
				fmt.Printf("Failed to refresh existing token: %v. Re-auth required.\n", err)
			}
		}
	}

	// Restore persisted settings (game, drops watcher)
	if settings, err := auth.GetSettings(); err == nil {
		s.mu.Lock()
		s.gameName = settings.GameName
		s.dropsActive = settings.DropsActive
		s.mu.Unlock()
		if settings.DropsActive && settings.GameName != "" {
			s.startScraper()
			go s.scrapeDrops()
		}
	}

	server := &http.Server{
		Addr:    ":" + s.port,
		Handler: s.routes(),
	}

	// Open dashboard in default browser on launch
	go func() {
		time.Sleep(1 * time.Second)
		_ = s.openBrowser(fmt.Sprintf("http://localhost:%s", s.port))
	}()

	fmt.Printf("Starting Web UI Dashboard on http://localhost:%s...\n", s.port)
	fmt.Printf("Dashboard is also reachable from the LAN at http://<this-machine-ip>:%s; credential setup and logout only work from this machine.\n", s.port)
	return server.ListenAndServe()
}

// routes builds the HTTP handler chain for the dashboard.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/browser-test", s.handleBrowserTest)
	mux.HandleFunc("/callback", loopbackOnly(s.handleCallback))
	mux.HandleFunc("/api/setup", loopbackOnly(sameOriginOnly(s.handleSetup)))
	mux.HandleFunc("/api/channels", sameOriginOnly(s.handleChannels))
	mux.HandleFunc("/api/logout", loopbackOnly(sameOriginOnly(s.handleLogout)))
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/drops/game", sameOriginOnly(s.handleUpdateGame))
	mux.HandleFunc("/api/drops/game/clear", sameOriginOnly(s.handleClearGame))
	mux.HandleFunc("/api/drops/toggle", sameOriginOnly(s.handleToggleDrops))
	mux.HandleFunc("/api/drops/auto", sameOriginOnly(s.handleAutoWatch))
	mux.HandleFunc("/api/browser/open", sameOriginOnly(s.handleBrowserOpen))
	mux.HandleFunc("/api/browser/close", sameOriginOnly(s.handleBrowserClose))
	return hostIsLocalOrIP(mux)
}

// hostIsLocalOrIP rejects requests whose Host header is not localhost or an IP literal.
func hostIsLocalOrIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host != "localhost" && net.ParseIP(host) == nil {
			http.Error(w, "Forbidden: invalid Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackOnly restricts a handler to requests originating from the host machine.
func loopbackOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			http.Error(w, "Credential changes are only allowed from the host machine.", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// sameOriginOnly rejects cross-site POST requests via Sec-Fetch-Site/Origin headers.
func sameOriginOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s := r.Header.Get("Sec-Fetch-Site"); s == "cross-site" || s == "same-site" {
			http.Error(w, "Forbidden: cross-site request", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "Forbidden: cross-origin request", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// isLoopbackRequest reports whether the request's remote address is a loopback IP.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GetChannelUpdateChan returns the channel that notifies of monitored channel updates.
func (s *Server) GetChannelUpdateChan() <-chan struct{} {
	return s.channelUpdateChan
}

// notifyChannelUpdate signals the main loop that the monitored channel set changed.
func (s *Server) notifyChannelUpdate() {
	select {
	case s.channelUpdateChan <- struct{}{}:
	default:
	}
}

// GetLogoutChan returns the channel that notifies of explicit logout requests.
func (s *Server) GetLogoutChan() <-chan struct{} {
	return s.logoutChan
}

// SetTwitchClient updates the internal twitch client reference.
func (s *Server) SetTwitchClient(client *twitch.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.twitchClient = client
}

// SetWSManager updates the internal websocket manager reference.
func (s *Server) SetWSManager(wsMgr *twitch.WebSocketManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wsMgr = wsMgr
}

// IsAuthenticated returns true if the server has valid Twitch API credentials.
func (s *Server) IsAuthenticated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isAuthenticated
}

// GetTwitchClient returns the current Twitch Client instance.
func (s *Server) GetTwitchClient() *twitch.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.twitchClient
}

// GetMonitoredIDs returns the IDs of the monitored channels in priority order.
func (s *Server) GetMonitoredIDs() []string {
	return s.monitor.IDs()
}

// OnStreamOnline is the EventSub stream.online callback.
func (s *Server) OnStreamOnline(ev *twitch.WSEvent) {
	if s.monitor.SetLive(ev.BroadcasterUserID, true) {
		s.applyAction(s.monitor.Reconcile())
	}
}

// OnStreamOffline is the EventSub stream.offline callback.
func (s *Server) OnStreamOffline(ev *twitch.WSEvent) {
	if s.monitor.SetLive(ev.BroadcasterUserID, false) {
		s.applyAction(s.monitor.Reconcile())
	}
}

// applyAction performs the browser side effects of a monitor.Action.
func (s *Server) applyAction(a monitor.Action) {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	// Close first whenever we're about to open and a browser is already running,
	// so a drops/manual window can't block a monitored stream.
	if a.Close || (a.Open != nil && s.browserMgr.IsRunning()) {
		_ = s.browserMgr.CloseStream()
	}
	if a.Open != nil {
		s.stopWatchTicker() // monitored channels take priority over a drops auto-watch session
		fmt.Printf("[AutoTTV] Opening stream for %s\n", a.Open.Login)
		if err := s.browserMgr.OpenStream("https://twitch.tv/" + a.Open.Login); err != nil {
			fmt.Printf("[AutoTTV] Error opening browser stream: %v\n", err)
		}
	}
}

// handleBrowserExit runs when the stream window's process exits on its own
// (user closed it, crash). It reopens the monitored channel after reopenDelay,
// unless the browser keeps dying right after launch.
func (s *Server) handleBrowserExit(url string, lifetime time.Duration) {
	cur := s.monitor.Current()
	if cur == "" || url != "https://twitch.tv/"+cur {
		// A manual test window or a drops window closing isn't our concern.
		return
	}

	s.mu.Lock()
	if lifetime < quickExitThreshold {
		if s.quickExitLogin == cur {
			s.quickExits++
		} else {
			s.quickExits = 1
			s.quickExitLogin = cur
		}
	} else {
		s.quickExits = 0
	}
	quickExits := s.quickExits
	if quickExits >= maxQuickExits {
		s.quickExits = 0
	}
	delay := s.reopenDelay
	s.mu.Unlock()

	if quickExits >= maxQuickExits {
		s.monitor.MarkFailed(cur)
		fmt.Printf("[AutoTTV] Browser for %s closed %d times right after launch; not reopening until the channel changes\n", cur, quickExits)
		return
	}

	fmt.Printf("[AutoTTV] Stream window for %s closed unexpectedly; reopening in %v\n", cur, delay)
	s.monitor.HoldReopen(cur, time.Now().Add(delay))
	// A stale timer firing is harmless: Reconcile still honors the hold.
	time.AfterFunc(delay, func() {
		s.applyAction(s.monitor.Reconcile())
	})
}

// ResyncLiveStatus refetches live status and reconciles once. Called after the
// EventSub WebSocket resubscribes on a new session, to catch missed events.
func (s *Server) ResyncLiveStatus() {
	client := s.GetTwitchClient()
	if client == nil {
		return
	}
	ids := s.monitor.IDs()
	if len(ids) == 0 {
		return
	}
	live, err := client.GetLiveUserIDs(ids)
	if err != nil {
		// Don't treat channels as offline on error: that would close a live stream.
		fmt.Printf("[AutoTTV] Failed to resync live status: %v\n", err)
		return
	}
	s.monitor.SetLiveMap(live)
	s.applyAction(s.monitor.Reconcile())
	fmt.Println("[AutoTTV] Resynced live status after reconnect")
}

// loadChannels resolves logins to monitor channels (with live status), applies
// the result, and notifies the main loop. Used by Start and handleCallback.
func (s *Server) loadChannels(client *twitch.Client, logins []string) (unknown []string, err error) {
	logins, err = normalizeLogins(logins)
	if err != nil {
		return nil, err
	}
	if len(logins) > monitor.MaxChannels {
		return nil, fmt.Errorf("a maximum of %d channels can be monitored", monitor.MaxChannels)
	}
	if len(logins) == 0 {
		s.applyChannels(nil)
		return nil, nil
	}
	chs, unknown, err := s.resolveChannels(client, logins)
	if err != nil {
		return unknown, err
	}
	s.applyChannels(chs)
	return unknown, nil
}

// resolveChannels normalizes logins and resolves them to monitor channels
// (including live status) without mutating server state.
func (s *Server) resolveChannels(client *twitch.Client, logins []string) (chs []monitor.Channel, unknown []string, err error) {
	users, err := client.GetUsersByLogin(logins)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve channels: %w", err)
	}

	idByLogin := make(map[string]string, len(users))
	for _, u := range users {
		idByLogin[u.Login] = u.ID
	}

	var ids []string
	for _, login := range logins {
		id, ok := idByLogin[login]
		if !ok {
			unknown = append(unknown, login)
			continue
		}
		ids = append(ids, id)
		chs = append(chs, monitor.Channel{Login: login, ID: id})
	}

	live, err := client.GetLiveUserIDs(ids)
	if err != nil {
		fmt.Printf("Failed to fetch live status (treating all as offline): %v\n", err)
		live = nil
	}
	for i := range chs {
		chs[i].Live = live[chs[i].ID]
	}
	return chs, unknown, nil
}

// applyChannels stores the resolved channels, reconciles browser state, and notifies the main loop.
func (s *Server) applyChannels(chs []monitor.Channel) {
	s.monitor.SetChannels(chs)
	s.applyAction(s.monitor.Reconcile())
	s.notifyChannelUpdate()
}

// normalizeLogins trims, lowercases, drops empties, and dedupes preserving first occurrence.
func normalizeLogins(logins []string) ([]string, error) {
	seen := make(map[string]bool, len(logins))
	out := make([]string, 0, len(logins))
	for _, l := range logins {
		login, err := parseChannelLogin(l)
		if err != nil {
			return nil, err
		}
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		out = append(out, login)
	}
	return out, nil
}

// parseChannelLogin accepts a bare Twitch login or a twitch.tv channel URL
// and returns the lowercased login.
func parseChannelLogin(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", nil
	}
	raw := in
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil && twitchHostRe.MatchString(strings.ToLower(u.Host)) {
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		in = segs[0]
	}
	login := strings.ToLower(in)
	if !loginRe.MatchString(login) {
		return "", fmt.Errorf("invalid channel name or URL: %q", strings.TrimSpace(in))
	}
	return login, nil
}

func (s *Server) handleBrowserTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(browserTestHTML))
}

func (s *Server) handleBrowserOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetURL := "https://twitch.tv"
	login, err := parseChannelLogin(r.FormValue("channel"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if login != "" {
		targetURL = "https://twitch.tv/" + login
	}

	err = s.browserMgr.OpenStream(targetURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleBrowserClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Pausing a monitored channel must not interleave with applyAction;
	// CloseStream does not take actionMu, so holding it here is safe.
	s.actionMu.Lock()
	if cur := s.monitor.Current(); cur != "" {
		s.monitor.Pause(cur)
	} else if reopenLogin, _, _, _ := s.monitor.Status(); reopenLogin != "" {
		s.monitor.Pause(reopenLogin)
	}
	err := s.browserMgr.CloseStream()
	s.actionMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data := dashboardData{
		IsAuthenticated: s.isAuthenticated,
		IsLocal:         isLoopbackRequest(r),
		HasStoredCreds:  s.clientId != "" && s.clientSecret != "",
		ClientId:        s.clientId,
		GameName:        s.gameName,
		BrowserRunning:  s.browserMgr.IsRunning(),
		Port:            s.port,
		Channels:        s.monitor.Channels(),
	}
	s.mu.Unlock()

	var buf bytes.Buffer
	if err := dashboardTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Failed to render dashboard", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(buf.Bytes())
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientId := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")

	if clientId == "" {
		http.Error(w, "Client ID is required", http.StatusBadRequest)
		return
	}

	if clientSecret == "" {
		s.mu.Lock()
		canReuse := clientId == s.clientId && s.clientSecret != ""
		s.mu.Unlock()
		if !canReuse {
			http.Error(w, "Client Secret is required", http.StatusBadRequest)
			return
		}
		s.startOAuth(w, r, clientId)
		return
	}

	s.mu.Lock()
	s.clientId = clientId
	s.clientSecret = clientSecret
	s.mu.Unlock()

	// Store them temporarily in keyring (StartOAuthFlow will permanently save them on success)
	_ = auth.SaveCredentials(clientId, clientSecret, "")

	s.startOAuth(w, r, clientId)
}

// startOAuth generates the OAuth state and redirects to the Twitch authorize URL.
func (s *Server) startOAuth(w http.ResponseWriter, r *http.Request, clientId string) {
	redirectURI := fmt.Sprintf("http://localhost:%s/callback", s.port)
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		http.Error(w, "Failed to generate OAuth state", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(stateBytes)

	s.mu.Lock()
	s.oauthState = state
	s.mu.Unlock()

	authURL := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=&state=%s",
		url.QueryEscape(clientId),
		url.QueryEscape(redirectURI),
		url.QueryEscape(state),
	)

	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	expectedState := s.oauthState
	s.mu.Unlock()

	if expectedState == "" || r.URL.Query().Get("state") != expectedState {
		http.Error(w, "OAuth state mismatch", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.oauthState = ""
	s.mu.Unlock()

	code := r.URL.Query().Get("code")

	if code == "" {
		http.Error(w, "Authorization code is missing", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	cid := s.clientId
	csec := s.clientSecret
	s.mu.Unlock()

	// Exchange code for token
	data := url.Values{}
	data.Set("client_id", cid)
	data.Set("client_secret", csec)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", fmt.Sprintf("http://localhost:%s/callback", s.port))

	resp, err := http.PostForm("https://id.twitch.tv/oauth2/token", data)
	if err != nil {
		http.Error(w, "Token exchange request failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		http.Error(w, fmt.Sprintf("Twitch token exchange failed: %v", errResp), http.StatusBadRequest)
		return
	}

	var tokenResp auth.TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		http.Error(w, "Failed to decode response: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Save securely
	if err := auth.SaveCredentials(cid, csec, tokenResp.RefreshToken); err != nil {
		http.Error(w, "Failed to securely save credentials: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.accessToken = tokenResp.AccessToken
	s.isAuthenticated = true
	s.twitchClient = twitch.NewClient(cid, csec, tokenResp.AccessToken, tokenResp.RefreshToken)
	s.mu.Unlock()

	// Try to restore previously monitored channels if they were saved
	logins, _ := auth.GetMonitoredChannels()
	if len(logins) > 0 {
		if _, err := s.loadChannels(s.twitchClient, logins); err != nil {
			fmt.Printf("Failed to restore monitored channels: %v\n", err)
		}
	}

	// Trigger setup complete redirect to dashboard
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Channels []string `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	logins, err := normalizeLogins(body.Channels)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(logins) > monitor.MaxChannels {
		http.Error(w, fmt.Sprintf("a maximum of %d channels can be monitored", monitor.MaxChannels), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	client := s.twitchClient
	s.mu.Unlock()

	if client == nil {
		http.Error(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var chs []monitor.Channel
	var unknown []string
	if len(logins) > 0 {
		chs, unknown, err = s.resolveChannels(client, logins)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(unknown) > 0 {
			http.Error(w, "Unknown channel(s): "+strings.Join(unknown, ", "), http.StatusBadRequest)
			return
		}
	}

	_ = auth.SaveMonitoredChannels(logins)
	s.applyChannels(chs)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"channels": s.monitor.Channels(),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Wipe credentials from OS Keyring
	_ = auth.ClearCredentials()

	s.mu.Lock()
	s.isAuthenticated = false
	s.accessToken = ""
	s.clientId = ""
	s.clientSecret = ""
	s.mu.Unlock()

	s.monitor.SetChannels(nil)
	s.applyAction(s.monitor.Reconcile())

	// Close the stream window if active
	_ = s.browserMgr.CloseStream()

	// Send signal to main to stop current websocket
	select {
	case s.logoutChan <- struct{}{}:
	default:
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	browserRunning := s.browserMgr.IsRunning()

	s.mu.Lock()
	needStop := !browserRunning && s.autoCampaign != ""
	s.mu.Unlock()

	// Update active watch state if browser is not running anymore
	if needStop {
		s.stopWatchTicker()
	}

	reopenLogin, reopenAt, paused, failed := s.monitor.Status()
	var reopenAtMs int64
	if !reopenAt.IsZero() {
		reopenAtMs = reopenAt.UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": s.isAuthenticated,
		"channels":      s.monitor.Channels(),
		"current":       s.monitor.Current(),
		"is_live":       s.monitor.Desired() != nil,
		"browser_open":  browserRunning,
		"reopen_login":  reopenLogin,
		"reopen_at":     reopenAtMs,
		"paused":        paused,
		"reopen_failed": failed,
		// Drops Watching State
		"game_name":     s.gameName,
		"drops_active":  s.dropsActive,
		"campaigns":     s.campaigns,
		"auto_campaign": s.autoCampaign,
	})
}

func (s *Server) handleUpdateGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	game := strings.TrimSpace(r.FormValue("game"))
	if game == "" {
		http.Error(w, "Game name cannot be empty", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.gameName = game
	s.mu.Unlock()

	s.persistSettings()

	// Perform an immediate scrape to update campaigns
	s.scrapeDrops()

	// Redirect back to dashboard
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleClearGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	s.gameName = ""
	s.campaigns = nil
	s.dropsActive = false
	s.mu.Unlock()

	s.stopScraper()
	s.stopWatchTicker()
	// Only close the browser for drops; a monitored channel's window is not ours to kill here.
	if s.monitor.Current() == "" {
		_ = s.browserMgr.CloseStream()
	}

	s.persistSettings()

	// Redirect back to dashboard
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleToggleDrops(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	activeStr := r.FormValue("active")
	active := activeStr == "true" || activeStr == "on" || activeStr == "1"

	s.mu.Lock()
	s.dropsActive = active
	s.mu.Unlock()

	if active {
		s.startScraper()
		s.scrapeDrops()
	} else {
		s.stopScraper()
		s.stopWatchTicker()
		if s.monitor.Current() == "" {
			_ = s.browserMgr.CloseStream()
		}
	}

	s.persistSettings()

	// Redirect back to dashboard
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleAutoWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	campaignName := r.FormValue("campaign")
	action := r.FormValue("action") // "start" or "stop"

	s.mu.Lock()
	client := s.twitchClient
	game := s.gameName
	s.mu.Unlock()

	if client == nil {
		http.Error(w, "Twitch client not initialized", http.StatusInternalServerError)
		return
	}

	if action == "stop" {
		s.stopWatchTicker()
		if s.monitor.Current() == "" {
			_ = s.browserMgr.CloseStream()
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if s.monitor.Desired() != nil {
		http.Error(w, "A monitored channel is live; auto-watch is paused while it is being watched", http.StatusConflict)
		return
	}

	// Find the campaign
	s.mu.Lock()
	var campaign *drops.Campaign
	for i, c := range s.campaigns {
		if c.Name == campaignName {
			campaign = &s.campaigns[i]
			break
		}
	}
	s.mu.Unlock()

	if campaign == nil {
		http.Error(w, "Campaign not found", http.StatusBadRequest)
		return
	}

	// Determine streamer URL
	var streamURL string
	var selectedStreamer string

	isAllChannels := len(campaign.Channels) == 1 && campaign.Channels[0] == "all"

	if isAllChannels {
		// Get Game ID
		gameInfo, err := client.GetGameByName(game)
		if err != nil {
			http.Error(w, "Failed to find game on Twitch: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Get top streams
		streams, err := client.GetTopStreamsByGame(gameInfo.ID)
		if err != nil {
			http.Error(w, "Failed to get top streams: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if len(streams) == 0 {
			http.Error(w, "No active streams found for this category on Twitch", http.StatusNotFound)
			return
		}

		// Pick at random
		idx := time.Now().UnixNano() % int64(len(streams))
		stream := streams[idx]
		selectedStreamer = stream.UserLogin
		streamURL = "https://twitch.tv/" + stream.UserLogin
	} else {
		// Specific channels, check live status of participating channels
		var liveStreamers []string
		for _, ch := range campaign.Channels {
			stream, err := client.GetStreamByUser(ch)
			if err == nil && stream != nil {
				liveStreamers = append(liveStreamers, stream.UserLogin)
			}
		}

		if len(liveStreamers) == 0 {
			http.Error(w, "None of the participating channels are currently live", http.StatusNotFound)
			return
		}

		// Pick at random
		idx := time.Now().UnixNano() % int64(len(liveStreamers))
		selectedStreamer = liveStreamers[idx]
		streamURL = "https://twitch.tv/" + selectedStreamer
	}

	// Open stream
	if err := s.browserMgr.OpenStream(streamURL); err != nil {
		http.Error(w, "Failed to open browser: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start watch ticker
	s.startWatchTicker(campaignName)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"streamer": selectedStreamer,
		"url":      streamURL,
	})
}

func (s *Server) startScraper() {
	s.mu.Lock()
	if s.scrapeTicker != nil {
		s.mu.Unlock()
		return
	}
	s.scrapeTicker = time.NewTicker(s.getRandomScrapeInterval())
	s.scrapeDone = make(chan struct{})
	s.mu.Unlock()

	go func() {
		for {
			select {
			case <-s.scrapeTicker.C:
				s.scrapeDrops()
				// Reset to a new random interval
				s.mu.Lock()
				if s.scrapeTicker != nil {
					s.scrapeTicker.Reset(s.getRandomScrapeInterval())
				}
				s.mu.Unlock()
			case <-s.scrapeDone:
				return
			}
		}
	}()
}

func (s *Server) stopScraper() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scrapeTicker != nil {
		s.scrapeTicker.Stop()
		s.scrapeTicker = nil
		close(s.scrapeDone)
	}
}

func (s *Server) getRandomScrapeInterval() time.Duration {
	// A random time between 1 and 2 hours
	randMins := 60 + (time.Now().UnixNano() % 60)
	return time.Duration(randMins) * time.Minute
}

func (s *Server) scrapeDrops() {
	s.mu.Lock()
	game := s.gameName
	s.mu.Unlock()

	if game == "" {
		return
	}

	campaigns, err := drops.GetActiveDrops(game)
	if err != nil {
		fmt.Printf("Error scraping active drops for game '%s': %v\n", game, err)
		return
	}

	s.mu.Lock()
	// Merge existing watch progress
	for i, newC := range campaigns {
		for _, oldC := range s.campaigns {
			if oldC.Name == newC.Name {
				campaigns[i].WatchTime = oldC.WatchTime
				campaigns[i].AutoWatching = oldC.AutoWatching
				break
			}
		}
	}
	s.campaigns = campaigns
	s.lastScrape = time.Now()
	s.mu.Unlock()
}

func (s *Server) startWatchTicker(campaignName string) {
	s.mu.Lock()
	if s.watchTicker != nil {
		s.mu.Unlock()
		return
	}
	s.autoCampaign = campaignName
	s.watchStart = time.Now()
	s.watchTicker = time.NewTicker(1 * time.Second)
	s.watchDone = make(chan struct{})
	s.mu.Unlock()

	go func() {
		lastTick := time.Now()
		for {
			select {
			case <-s.watchTicker.C:
				s.mu.Lock()
				// Check if browser is still running
				if !s.browserMgr.IsRunning() {
					s.mu.Unlock()
					s.stopWatchTicker()
					return
				}

				// Increment watch time of the active campaign
				now := time.Now()
				elapsed := int(now.Sub(lastTick).Seconds())
				lastTick = now

				for i, c := range s.campaigns {
					if c.Name == s.autoCampaign {
						s.campaigns[i].WatchTime += elapsed
						s.campaigns[i].AutoWatching = true
						break
					}
				}
				s.mu.Unlock()
			case <-s.watchDone:
				return
			}
		}
	}()
}

func (s *Server) stopWatchTicker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchTicker != nil {
		s.watchTicker.Stop()
		s.watchTicker = nil
		close(s.watchDone)
	}
	// Uncheck auto for all campaigns
	for i := range s.campaigns {
		s.campaigns[i].AutoWatching = false
	}
	s.autoCampaign = ""
}

func (s *Server) openBrowser(urlStr string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", urlStr)
	case "darwin":
		cmd = exec.Command("open", urlStr)
	default:
		cmd = exec.Command("xdg-open", urlStr)
	}
	return cmd.Start()
}

func (s *Server) persistSettings() {
	s.mu.Lock()
	settings := auth.Settings{
		GameName:    s.gameName,
		DropsActive: s.dropsActive,
	}
	s.mu.Unlock()
	_ = auth.SaveSettings(settings)
}

// Embedded static HTML files to keep the binary lightweight and dependency-free.

//go:embed dashboard.html
var dashboardHTML string

var dashboardTmpl = template.Must(template.New("dashboard").Parse(dashboardHTML))

var loginRe = regexp.MustCompile(`^[a-z0-9_]{1,25}$`)
var twitchHostRe = regexp.MustCompile(`^(www\.|m\.)?twitch\.tv(:\d+)?$`)

type dashboardData struct {
	IsAuthenticated bool
	Channels        []monitor.Channel
	IsLocal         bool
	HasStoredCreds  bool
	ClientId        string
	GameName        string
	BrowserRunning  bool
	Port            string
}

//go:embed browser_test.html
var browserTestHTML string
