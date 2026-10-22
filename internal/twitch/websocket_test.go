package twitch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoTtv/internal/twitch"
	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func TestWebSocketManager_Lifecycle(t *testing.T) {
	// Channels to verify callbacks
	onlineChan := make(chan string, 1)
	offlineChan := make(chan string, 1)

	onOnline := func(event *twitch.WSEvent) {
		onlineChan <- event.BroadcasterUserLogin
	}
	onOffline := func(event *twitch.WSEvent) {
		offlineChan <- event.BroadcasterUserLogin
	}

	// 1. Create mock EventSub API server for subscription calls
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/eventsub/subscriptions" {
			t.Errorf("unexpected API path: %s", r.URL.Path)
		}
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []interface{}{},
			})
			return
		}
		if r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "mock-sub-1"},
				},
			})
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}))
	defer apiServer.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = apiServer.URL

	// 2. Create mock WebSocket server
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		// Send session_welcome
		welcomeMsg := map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "welcome-msg-id",
				"message_type":      "session_welcome",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":                        "session-id-123",
					"status":                    "connected",
					"keepalive_timeout_seconds": 10,
					"connected_at":              time.Now().Format(time.RFC3339),
				},
			},
		}
		_ = conn.WriteJSON(welcomeMsg)

		// Wait briefly, then send notification (stream.online)
		time.Sleep(50 * time.Millisecond)
		onlineMsg := map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":           "online-msg-id",
				"message_type":         "notification",
				"message_timestamp":    time.Now().Format(time.RFC3339),
				"subscription_type":    "stream.online",
				"subscription_version": "1",
			},
			"payload": map[string]interface{}{
				"event": map[string]interface{}{
					"broadcaster_user_id":    "10137",
					"broadcaster_user_login": "shroud",
					"broadcaster_user_name":  "shroud",
				},
			},
		}
		_ = conn.WriteJSON(onlineMsg)

		// Wait briefly, then send notification (stream.offline)
		time.Sleep(50 * time.Millisecond)
		offlineMsg := map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":           "offline-msg-id",
				"message_type":         "notification",
				"message_timestamp":    time.Now().Format(time.RFC3339),
				"subscription_type":    "stream.offline",
				"subscription_version": "1",
			},
			"payload": map[string]interface{}{
				"event": map[string]interface{}{
					"broadcaster_user_id":    "10137",
					"broadcaster_user_login": "shroud",
					"broadcaster_user_name":  "shroud",
				},
			},
		}
		_ = conn.WriteJSON(offlineMsg)

		// Keep connection alive for a bit
		time.Sleep(200 * time.Millisecond)
	}))
	defer wsServer.Close()

	// Convert HTTP URL to WS/WSS URL
	wsURL := strings.Replace(wsServer.URL, "http://", "ws://", 1)

	// Create and start WebSocketManager
	wm := twitch.NewWebSocketManager(client, []string{"10137"}, onOnline, onOffline)
	wm.WSURL = wsURL

	err := wm.Start()
	if err != nil {
		t.Fatalf("failed to start WebSocketManager: %v", err)
	}
	defer wm.Stop()

	// Wait for callbacks with timeouts
	select {
	case user := <-onlineChan:
		if user != "shroud" {
			t.Errorf("expected online notification for shroud, got %s", user)
		}
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for online notification")
	}

	select {
	case user := <-offlineChan:
		if user != "shroud" {
			t.Errorf("expected offline notification for shroud, got %s", user)
		}
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for offline notification")
	}
}

func TestWebSocketManager_SetBroadcasterID_ClearsSubscriptions(t *testing.T) {
	deletedSubIDs := make(chan string, 10)
	onlineChan := make(chan string, 10)

	onOnline := func(event *twitch.WSEvent) {
		onlineChan <- event.BroadcasterUserLogin
	}

	// 1. Mock EventSub API server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []interface{}{},
			})
			return
		}
		if r.Method == "POST" {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			broadcasterID := ""
			if cond, ok := body["condition"].(map[string]interface{}); ok {
				broadcasterID = cond["broadcaster_user_id"].(string)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "sub-id-" + broadcasterID},
				},
			})
			return
		}
		if r.Method == "DELETE" {
			id := r.URL.Query().Get("id")
			deletedSubIDs <- id
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}))
	defer apiServer.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = apiServer.URL

	// 2. Mock WebSocket server
	var wsConn *websocket.Conn
	welcomeSent := make(chan struct{})
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection: %v", err)
			return
		}
		wsConn = conn
		defer conn.Close()

		// Send session_welcome
		_ = conn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "welcome-msg-id",
				"message_type":      "session_welcome",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":                        "session-id-456",
					"status":                    "connected",
					"keepalive_timeout_seconds": 10,
					"connected_at":              time.Now().Format(time.RFC3339),
				},
			},
		})
		// Hand the connection over to the test goroutine; no further writes happen here.
		close(welcomeSent)

		// Keep connection open
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	wsURL := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	wm := twitch.NewWebSocketManager(client, []string{"10137"}, onOnline, nil)
	wm.WSURL = wsURL

	if err := wm.Start(); err != nil {
		t.Fatalf("failed to start WebSocketManager: %v", err)
	}
	defer wm.Stop()

	// Allow session_welcome and subscription creation to occur
	select {
	case <-welcomeSent:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for mock server to send session_welcome")
	}
	time.Sleep(100 * time.Millisecond)

	// Change broadcaster to "99999"
	if err := wm.SetBroadcasterIDs([]string{"99999"}); err != nil {
		t.Fatalf("failed to SetBroadcasterIDs: %v", err)
	}

	// Verify that the old subscription ID for 10137 was deleted
	select {
	case id := <-deletedSubIDs:
		if id != "sub-id-10137" {
			t.Errorf("expected deleted sub ID 'sub-id-10137', got '%s'", id)
		}
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for subscription DELETE request")
	}

	// Send an event for the OLD channel 10137 and verify it is ignored
	if wsConn != nil {
		_ = wsConn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":           "rogue-msg-id",
				"message_type":         "notification",
				"message_timestamp":    time.Now().Format(time.RFC3339),
				"subscription_type":    "stream.online",
				"subscription_version": "1",
			},
			"payload": map[string]interface{}{
				"event": map[string]interface{}{
					"broadcaster_user_id":    "10137",
					"broadcaster_user_login": "shroud",
					"broadcaster_user_name":  "shroud",
				},
			},
		})
	}

	// Wait briefly and ensure no notification triggered for shroud
	select {
	case user := <-onlineChan:
		t.Fatalf("unexpected notification for old channel: %s", user)
	case <-time.After(100 * time.Millisecond):
		// Expected: no event received for old channel
	}

	// Now send an event for the NEW channel 99999 and verify it IS handled
	if wsConn != nil {
		_ = wsConn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":           "valid-msg-id",
				"message_type":         "notification",
				"message_timestamp":    time.Now().Format(time.RFC3339),
				"subscription_type":    "stream.online",
				"subscription_version": "1",
			},
			"payload": map[string]interface{}{
				"event": map[string]interface{}{
					"broadcaster_user_id":    "99999",
					"broadcaster_user_login": "newstreamer",
					"broadcaster_user_name":  "newstreamer",
				},
			},
		})
	}

	select {
	case user := <-onlineChan:
		if user != "newstreamer" {
			t.Errorf("expected newstreamer, got %s", user)
		}
	case <-time.After(1 * time.Second):
		t.Error("timed out waiting for notification on new channel")
	}
}

func TestWebSocketManager_MultipleBroadcasters(t *testing.T) {
	type subKey struct{ id, typ string }
	subCh := make(chan subKey, 10)

	// 1. Mock EventSub API server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []interface{}{},
			})
			return
		}
		if r.Method == "POST" {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			broadcasterID := ""
			if cond, ok := body["condition"].(map[string]interface{}); ok {
				broadcasterID, _ = cond["broadcaster_user_id"].(string)
			}
			typ, _ := body["type"].(string)
			subCh <- subKey{broadcasterID, typ}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "sub-id-" + broadcasterID + "-" + typ},
				},
			})
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}))
	defer apiServer.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = apiServer.URL

	// 2. Mock WebSocket server that sends session_welcome
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "welcome-msg-id",
				"message_type":      "session_welcome",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":                        "session-id-789",
					"status":                    "connected",
					"keepalive_timeout_seconds": 10,
					"connected_at":              time.Now().Format(time.RFC3339),
				},
			},
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	wsURL := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	wm := twitch.NewWebSocketManager(client, []string{"10137", "44455"}, nil, nil)
	wm.WSURL = wsURL

	if err := wm.Start(); err != nil {
		t.Fatalf("failed to start WebSocketManager: %v", err)
	}
	defer wm.Stop()

	// Expect exactly 4 subscription POSTs: both IDs x stream.online + stream.offline
	got := make(map[subKey]bool)
	for i := 0; i < 4; i++ {
		select {
		case k := <-subCh:
			got[k] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for subscription POST %d of 4 (got %v)", i+1, got)
		}
	}

	want := map[subKey]bool{
		{"10137", "stream.online"}:  true,
		{"10137", "stream.offline"}: true,
		{"44455", "stream.online"}:  true,
		{"44455", "stream.offline"}: true,
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing subscription for %v (got %v)", k, got)
		}
	}
}

func TestWebSocketManager_PurgesStaleSubscriptions(t *testing.T) {
	const sessionID = "session-current"

	var mu sync.Mutex
	var order []string
	var deletedIDs []string
	postsDone := make(chan struct{}, 2)

	record := func(entry string) {
		mu.Lock()
		order = append(order, entry)
		mu.Unlock()
	}

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			record("GET " + r.URL.Query().Get("type"))
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("type") == "stream.online" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": []map[string]interface{}{
						{
							"id":   "stale-1",
							"type": "stream.online",
							"transport": map[string]interface{}{
								"method":     "websocket",
								"session_id": "old-session",
							},
						},
						{
							"id":   "current-1",
							"type": "stream.online",
							"transport": map[string]interface{}{
								"method":     "websocket",
								"session_id": sessionID,
							},
						},
						{
							"id":   "hook-1",
							"type": "stream.online",
							"transport": map[string]interface{}{
								"method": "webhook",
							},
						},
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": []interface{}{},
				})
			}
		case "DELETE":
			id := r.URL.Query().Get("id")
			record("DELETE " + id)
			mu.Lock()
			deletedIDs = append(deletedIDs, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "POST":
			record("POST")
			postsDone <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "new-sub"},
				},
			})
		}
	}))
	defer apiServer.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = apiServer.URL

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "welcome-msg-id",
				"message_type":      "session_welcome",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":                        sessionID,
					"status":                    "connected",
					"keepalive_timeout_seconds": 10,
					"connected_at":              time.Now().Format(time.RFC3339),
				},
			},
		})

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	wsURL := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	wm := twitch.NewWebSocketManager(client, []string{"10137"}, nil, nil)
	wm.WSURL = wsURL

	if err := wm.Start(); err != nil {
		t.Fatalf("failed to start WebSocketManager: %v", err)
	}
	defer wm.Stop()

	// Wait for both subscription POSTs (stream.online + stream.offline)
	for i := 0; i < 2; i++ {
		select {
		case <-postsDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for subscription POST %d of 2", i+1)
		}
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(deletedIDs) != 1 || deletedIDs[0] != "stale-1" {
		t.Errorf("expected only 'stale-1' to be deleted, got %v", deletedIDs)
	}

	firstPost := -1
	firstDelete := -1
	for i, entry := range order {
		if entry == "POST" && firstPost == -1 {
			firstPost = i
		}
		if entry == "DELETE stale-1" && firstDelete == -1 {
			firstDelete = i
		}
	}
	if firstDelete == -1 || firstPost == -1 || firstDelete > firstPost {
		t.Errorf("expected DELETE of stale sub before first POST, order: %v", order)
	}
}

func TestWebSocketManager_ReconnectMigrationKeepsSession(t *testing.T) {
	var connCountA int32
	var postCount int32

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []interface{}{},
			})
		case "POST":
			atomic.AddInt32(&postCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{"id": "sub-1"},
				},
			})
		case "DELETE":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer apiServer.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = apiServer.URL

	welcome := func(id string) map[string]interface{} {
		return map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "welcome-" + id,
				"message_type":      "session_welcome",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":                        id,
					"status":                    "connected",
					"keepalive_timeout_seconds": 10,
					"connected_at":              time.Now().Format(time.RFC3339),
				},
			},
		}
	}

	// Server B: accepts the migrated connection, re-sends the same session ID.
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection on B: %v", err)
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(welcome("s1"))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer serverB.Close()
	wsURLB := strings.Replace(serverB.URL, "http://", "ws://", 1)

	// Server A: sends welcome then a session_reconnect pointing at B.
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&connCountA, 1)
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("failed to upgrade connection on A: %v", err)
			return
		}
		defer conn.Close()

		_ = conn.WriteJSON(welcome("s1"))
		time.Sleep(50 * time.Millisecond)
		_ = conn.WriteJSON(map[string]interface{}{
			"metadata": map[string]interface{}{
				"message_id":        "reconnect-msg-id",
				"message_type":      "session_reconnect",
				"message_timestamp": time.Now().Format(time.RFC3339),
			},
			"payload": map[string]interface{}{
				"session": map[string]interface{}{
					"id":            "s1",
					"status":        "reconnecting",
					"reconnect_url": wsURLB,
					"connected_at":  time.Now().Format(time.RFC3339),
				},
			},
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer serverA.Close()

	wsURLA := strings.Replace(serverA.URL, "http://", "ws://", 1)
	wm := twitch.NewWebSocketManager(client, []string{"10137"}, nil, nil)
	wm.WSURL = wsURLA

	if err := wm.Start(); err != nil {
		t.Fatalf("failed to start WebSocketManager: %v", err)
	}
	defer wm.Stop()

	time.Sleep(500 * time.Millisecond)

	if got := atomic.LoadInt32(&connCountA); got != 1 {
		t.Errorf("expected exactly 1 connection to server A, got %d", got)
	}
	if got := atomic.LoadInt32(&postCount); got != 2 {
		t.Errorf("expected exactly 2 subscription POSTs (no resubscribe after migration), got %d", got)
	}
}
