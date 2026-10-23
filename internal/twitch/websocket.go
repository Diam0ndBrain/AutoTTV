package twitch

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const defaultWebSocketURL = "wss://eventsub.wss.twitch.tv/ws"

// EventType defines Twitch EventSub types.
const (
	StreamOnline  = "stream.online"
	StreamOffline = "stream.offline"
)

// WSMessage represents the full payload sent by Twitch EventSub over WebSockets.
type WSMessage struct {
	Metadata WSMetadata `json:"metadata"`
	Payload  WSPayload  `json:"payload"`
}

type WSMetadata struct {
	MessageID           string    `json:"message_id"`
	MessageType         string    `json:"message_type"`
	MessageTimestamp    time.Time `json:"message_timestamp"`
	SubscriptionType    string    `json:"subscription_type,omitempty"`
	SubscriptionVersion string    `json:"subscription_version,omitempty"`
}

type WSPayload struct {
	Session      *WSSession      `json:"session,omitempty"`
	Subscription *WSSubscription `json:"subscription,omitempty"`
	Event        *WSEvent        `json:"event,omitempty"`
}

type WSSession struct {
	ID                      string    `json:"id"`
	Status                  string    `json:"status"`
	KeepaliveTimeoutSeconds int       `json:"keepalive_timeout_seconds"`
	ReconnectURL            string    `json:"reconnect_url,omitempty"`
	ConnectedAt             time.Time `json:"connected_at"`
}

type WSSubscription struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Type      string            `json:"type"`
	Version   string            `json:"version"`
	Condition map[string]string `json:"condition"`
}

type WSEvent struct {
	BroadcasterUserID    string    `json:"broadcaster_user_id"`
	BroadcasterUserLogin string    `json:"broadcaster_user_login"`
	BroadcasterUserName  string    `json:"broadcaster_user_name"`
	Type                 string    `json:"type,omitempty"` // "live" for stream.online
	StartedAt            time.Time `json:"started_at,omitempty"`
}

// WebSocketManager handles the lifecycle of the Twitch EventSub WebSocket.
type WebSocketManager struct {
	mu                sync.Mutex
	client            *Client
	conn              *websocket.Conn
	sessionID         string
	broadcasterIDs    []string
	subscriptions     []string
	onStreamOnline    func(event *WSEvent)
	onStreamOffline   func(event *WSEvent)
	stopChan          chan struct{}
	lastMessageTime   time.Time
	keepaliveTimeout  time.Duration
	isReconnecting    bool
	reconnectAttempts int
	WSURL             string // Exported for mocking/testing
	// OnResubscribed is called after subscriptions succeed on a NEW session
	// (not the first session, not a session_reconnect migration).
	OnResubscribed func()
}

// NewWebSocketManager creates an uninitialized WebSocket manager.
func NewWebSocketManager(client *Client, broadcasterIDs []string, onOnline, onOffline func(event *WSEvent)) *WebSocketManager {
	return &WebSocketManager{
		client:          client,
		broadcasterIDs:  broadcasterIDs,
		onStreamOnline:  onOnline,
		onStreamOffline: onOffline,
		stopChan:        make(chan struct{}),
		WSURL:           defaultWebSocketURL,
	}
}

// Start connects to the Twitch EventSub WebSocket server.
func (wm *WebSocketManager) Start() error {
	wm.mu.Lock()
	url := wm.WSURL
	wm.mu.Unlock()
	return wm.connect(url)
}

// Stop closes the WebSocket connection and stops the management loop.
func (wm *WebSocketManager) Stop() {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	select {
	case <-wm.stopChan:
		// Already stopped
		return
	default:
		close(wm.stopChan)
	}

	if wm.conn != nil {
		_ = wm.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = wm.conn.Close()
	}
}

// ClearSubscriptions removes all currently active subscriptions for this session from Twitch.
func (wm *WebSocketManager) ClearSubscriptions() {
	wm.mu.Lock()
	subs := wm.subscriptions
	wm.subscriptions = nil
	wm.mu.Unlock()

	if len(subs) > 0 {
		fmt.Printf("Clearing %d active EventSub subscription(s)...\n", len(subs))
		for _, subID := range subs {
			if err := wm.client.UnsubscribeEventSub(subID); err != nil {
				fmt.Printf("Failed to unsubscribe subscription ID %s: %v\n", subID, err)
			}
		}
	}
}

// SetBroadcasterIDs changes the targeted channel IDs we are monitoring and
// triggers fresh subscriptions on our active connection while clearing prior ones.
func (wm *WebSocketManager) SetBroadcasterIDs(broadcasterIDs []string) error {
	// Purge existing subscriptions on Twitch API
	wm.ClearSubscriptions()

	wm.mu.Lock()
	wm.broadcasterIDs = broadcasterIDs
	sessionID := wm.sessionID
	wm.mu.Unlock()

	if sessionID != "" && len(broadcasterIDs) > 0 {
		fmt.Printf("Subscribing to new broadcasters: %v\n", broadcasterIDs)
		if err := wm.subscribe(sessionID, broadcasterIDs); err != nil {
			return fmt.Errorf("failed to subscribe to new broadcasters: %w", err)
		}
	}
	return nil
}

func (wm *WebSocketManager) connect(wsURL string) error {
	wm.mu.Lock()
	wm.lastMessageTime = time.Now()
	wm.keepaliveTimeout = 15 * time.Second // Default fallback
	wm.mu.Unlock()

	fmt.Printf("Connecting to EventSub WebSocket: %s\n", wsURL)
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}

	wm.mu.Lock()
	wm.conn = conn
	wm.mu.Unlock()

	// Start reading messages
	go wm.readLoop(conn)

	// Start monitoring keepalive timeouts
	go wm.keepaliveMonitor(conn)

	return nil
}

func (wm *WebSocketManager) readLoop(conn *websocket.Conn) {
	for {
		_, messageBytes, err := conn.ReadMessage()
		if err != nil {
			wm.mu.Lock()
			isStopped := wm.isStopped()
			isCurrent := wm.conn == conn
			wm.mu.Unlock()

			if !isStopped && isCurrent {
				fmt.Printf("WebSocket read error: %v\n", err)
				wm.handleDisconnect()
			}
			return
		}

		wm.mu.Lock()
		wm.lastMessageTime = time.Now()
		wm.mu.Unlock()

		var msg WSMessage
		if err := json.Unmarshal(messageBytes, &msg); err != nil {
			fmt.Printf("Failed to unmarshal WS message: %v\n", err)
			continue
		}

		switch msg.Metadata.MessageType {
		case "session_welcome":
			wm.mu.Lock()
			wm.keepaliveTimeout = time.Duration(msg.Payload.Session.KeepaliveTimeoutSeconds+3) * time.Second
			broadcasterIDs := append([]string(nil), wm.broadcasterIDs...)
			if wm.sessionID == msg.Payload.Session.ID {
				// A migrated session keeps its existing subscriptions; nothing to redo.
				wm.reconnectAttempts = 0
				wm.mu.Unlock()
				fmt.Printf("Twitch EventSub WebSocket session migrated. Session ID: %s\n", msg.Payload.Session.ID)
				continue
			}
			hadSession := wm.sessionID != ""
			wm.sessionID = msg.Payload.Session.ID
			wm.subscriptions = nil
			wm.mu.Unlock()

			fmt.Printf("Twitch EventSub WebSocket session established. Session ID: %s\n", msg.Payload.Session.ID)

			if len(broadcasterIDs) == 0 {
				wm.mu.Lock()
				wm.reconnectAttempts = 0
				wm.mu.Unlock()
				continue
			}

			// Subscribe immediately (within the required 10-second window)
			go func() {
				if err := wm.subscribe(msg.Payload.Session.ID, broadcasterIDs); err != nil {
					fmt.Printf("Subscription creation failed: %v\n", err)
					return
				}
				wm.mu.Lock()
				wm.reconnectAttempts = 0
				wm.mu.Unlock()
				if hadSession && wm.OnResubscribed != nil {
					wm.OnResubscribed()
				}
			}()

		case "session_keepalive":
			// Do nothing, lastMessageTime is already updated

		case "session_reconnect":
			reconnectURL := msg.Payload.Session.ReconnectURL
			fmt.Printf("Received session_reconnect. Migrating to: %s\n", reconnectURL)
			go func() {
				if err := wm.connect(reconnectURL); err != nil {
					fmt.Printf("Failed to reconnect to migrated session: %v\n", err)
				} else {
					// Safely close the old connection once new is active
					_ = conn.Close()
				}
			}()

		case "notification":
			event := msg.Payload.Event
			if event == nil {
				continue
			}

			wm.mu.Lock()
			broadcasterIDs := append([]string(nil), wm.broadcasterIDs...)
			wm.mu.Unlock()

			if len(broadcasterIDs) > 0 {
				monitored := false
				for _, id := range broadcasterIDs {
					if event.BroadcasterUserID == id {
						monitored = true
						break
					}
				}
				if !monitored {
					fmt.Printf("[Twitch WS] Ignored notification for unmonitored channel %s\n", event.BroadcasterUserID)
					continue
				}
			}

			switch msg.Metadata.SubscriptionType {
			case StreamOnline:
				fmt.Printf("STREAM ONLINE: Channel %s (%s) is live!\n", event.BroadcasterUserName, event.BroadcasterUserID)
				if wm.onStreamOnline != nil {
					wm.onStreamOnline(event)
				}
			case StreamOffline:
				fmt.Printf("STREAM OFFLINE: Channel %s (%s) went offline.\n", event.BroadcasterUserName, event.BroadcasterUserID)
				if wm.onStreamOffline != nil {
					wm.onStreamOffline(event)
				}
			}

		case "revocation":
			fmt.Printf("Twitch revoked subscription for type: %s\n", msg.Payload.Subscription.Type)
		}
	}
}

func (wm *WebSocketManager) keepaliveMonitor(conn *websocket.Conn) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-wm.stopChan:
			return
		case <-ticker.C:
			wm.mu.Lock()
			lastMsg := wm.lastMessageTime
			timeout := wm.keepaliveTimeout
			activeConn := wm.conn
			wm.mu.Unlock()

			// If the connection in this routine is no longer the active one, terminate monitor
			if activeConn != conn {
				return
			}

			if time.Since(lastMsg) > timeout {
				fmt.Printf("Keepalive timeout exceeded (%v since last message). Reconnecting...\n", time.Since(lastMsg))
				_ = conn.Close() // This will trigger readLoop error and trigger handleDisconnect
				return
			}
		}
	}
}

// purgeStaleSubscriptions deletes leftover websocket-transport subscriptions from
// earlier sessions so new subscription POSTs do not hit the Twitch subscription limit.
func (wm *WebSocketManager) purgeStaleSubscriptions(sessionID string) {
	removed := 0
	for _, eventType := range []string{StreamOnline, StreamOffline} {
		subs, err := wm.client.GetEventSubSubscriptions(eventType)
		if err != nil {
			fmt.Printf("Failed to list %s subscriptions for cleanup: %v\n", eventType, err)
			continue
		}
		for _, sub := range subs {
			if sub.Transport.Method != "websocket" || sub.Transport.SessionID == sessionID {
				continue
			}
			if err := wm.client.UnsubscribeEventSub(sub.ID); err != nil {
				fmt.Printf("Failed to delete stale %s subscription %s: %v\n", eventType, sub.ID, err)
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		fmt.Printf("Removed %d stale EventSub subscription(s) from previous sessions.\n", removed)
	}
}

func (wm *WebSocketManager) subscribe(sessionID string, broadcasterIDs []string) error {
	wm.purgeStaleSubscriptions(sessionID)

	for _, broadcasterID := range broadcasterIDs {
		fmt.Printf("Registering EventSub subscriptions for broadcaster ID: %s...\n", broadcasterID)

		// Create StreamOnline subscription
		subOnlineID, err := wm.client.SubscribeEventSub(sessionID, StreamOnline, broadcasterID)
		if err != nil {
			return fmt.Errorf("failed to subscribe to stream.online for %s: %w", broadcasterID, err)
		}
		wm.mu.Lock()
		if subOnlineID != "" {
			wm.subscriptions = append(wm.subscriptions, subOnlineID)
		}
		wm.mu.Unlock()

		// Create StreamOffline subscription
		subOfflineID, err := wm.client.SubscribeEventSub(sessionID, StreamOffline, broadcasterID)
		if err != nil {
			return fmt.Errorf("failed to subscribe to stream.offline for %s: %w", broadcasterID, err)
		}
		wm.mu.Lock()
		if subOfflineID != "" {
			wm.subscriptions = append(wm.subscriptions, subOfflineID)
		}
		wm.mu.Unlock()
	}

	fmt.Println("EventSub subscriptions successfully created.")
	return nil
}

func (wm *WebSocketManager) handleDisconnect() {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if wm.isStopped() || wm.isReconnecting {
		return
	}

	wm.isReconnecting = true
	wm.reconnectAttempts++

	// Exponential backoff
	backoff := time.Duration(wm.reconnectAttempts*wm.reconnectAttempts) * time.Second
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}

	fmt.Printf("Reconnecting to WebSocket in %v (Attempt %d)...\n", backoff, wm.reconnectAttempts)

	go func() {
		time.Sleep(backoff)

		wm.mu.Lock()
		wm.isReconnecting = false
		isStop := wm.isStopped()
		wsURL := wm.WSURL
		wm.mu.Unlock()

		if isStop {
			return
		}

		if err := wm.connect(wsURL); err != nil {
			fmt.Printf("Reconnection attempt %d failed: %v\n", wm.reconnectAttempts, err)
			wm.handleDisconnect()
		}
	}()
}

func (wm *WebSocketManager) isStopped() bool {
	select {
	case <-wm.stopChan:
		return true
	default:
		return false
	}
}
