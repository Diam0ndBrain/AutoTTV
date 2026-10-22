package twitch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"autoTtv/internal/auth"
)

// Client interacts with the Twitch Helix REST API.
type Client struct {
	clientId     string
	clientSecret string
	accessToken  string
	refreshToken string
	httpClient   *http.Client
	BaseURL      string
	mu           sync.Mutex
}

// NewClient creates a new Twitch Helix API client.
func NewClient(clientId, clientSecret, accessToken, refreshToken string) *Client {
	return &Client{
		clientId:     clientId,
		clientSecret: clientSecret,
		accessToken:  accessToken,
		refreshToken: refreshToken,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		BaseURL:      "https://api.twitch.tv",
	}
}

// User represents a Twitch user.
type User struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	Name  string `json:"display_name"`
}

type usersResponse struct {
	Data []User `json:"data"`
}

// GetUserID retrieves the numeric user ID for a given twitch login/username.
func (c *Client) GetUserID(username string) (string, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/helix/users?login="+username, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create users request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return "", fmt.Errorf("users request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("users api returned status %d", resp.StatusCode)
	}

	var usersResp usersResponse
	if err := json.NewDecoder(resp.Body).Decode(&usersResp); err != nil {
		return "", fmt.Errorf("failed to decode users response: %w", err)
	}

	if len(usersResp.Data) == 0 {
		return "", fmt.Errorf("user '%s' not found", username)
	}

	return usersResp.Data[0].ID, nil
}

// GetUsersByLogin resolves up to 100 logins in one request. Returned order follows Twitch's response; callers re-order.
func (c *Client) GetUsersByLogin(logins []string) ([]User, error) {
	if len(logins) == 0 {
		return nil, nil
	}
	if len(logins) > 100 {
		return nil, fmt.Errorf("cannot resolve more than 100 logins per request")
	}

	q := url.Values{}
	for _, login := range logins {
		q.Add("login", login)
	}

	req, err := http.NewRequest("GET", c.BaseURL+"/helix/users?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create users request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("users request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("users api returned status %d", resp.StatusCode)
	}

	var usersResp usersResponse
	if err := json.NewDecoder(resp.Body).Decode(&usersResp); err != nil {
		return nil, fmt.Errorf("failed to decode users response: %w", err)
	}

	return usersResp.Data, nil
}

// Stream represents a Twitch active stream.
type Stream struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	UserLogin   string    `json:"user_login"`
	UserName    string    `json:"user_name"`
	Type        string    `json:"type"` // "live"
	Title       string    `json:"title"`
	ViewerCount int       `json:"viewer_count"`
	StartedAt   time.Time `json:"started_at"`
}

type streamsResponse struct {
	Data []Stream `json:"data"`
}

// IsLive checks if a broadcaster's stream is currently active/live.
func (c *Client) IsLive(userId string) (bool, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/helix/streams?user_id="+userId, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create streams request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return false, fmt.Errorf("streams request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("streams api returned status %d", resp.StatusCode)
	}

	var streamsResp streamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&streamsResp); err != nil {
		return false, fmt.Errorf("failed to decode streams response: %w", err)
	}

	for _, stream := range streamsResp.Data {
		if stream.Type == "live" {
			return true, nil
		}
	}

	return false, nil
}

// GetLiveUserIDs returns the set of user IDs (from the input) that are currently live.
func (c *Client) GetLiveUserIDs(userIDs []string) (map[string]bool, error) {
	live := make(map[string]bool)
	if len(userIDs) == 0 {
		return live, nil
	}
	if len(userIDs) > 100 {
		return nil, fmt.Errorf("cannot check more than 100 user IDs per request")
	}

	q := url.Values{}
	for _, id := range userIDs {
		q.Add("user_id", id)
	}
	q.Set("first", "100")

	req, err := http.NewRequest("GET", c.BaseURL+"/helix/streams?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create streams request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("streams request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("streams api returned status %d", resp.StatusCode)
	}

	var streamsResp streamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&streamsResp); err != nil {
		return nil, fmt.Errorf("failed to decode streams response: %w", err)
	}

	for _, stream := range streamsResp.Data {
		if stream.Type == "live" {
			live[stream.UserID] = true
		}
	}

	return live, nil
}

type eventSubResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// SubscribeEventSub registers an EventSub subscription for a specific event type and returns the subscription ID.
func (c *Client) SubscribeEventSub(sessionId, eventType, broadcasterUserId string) (string, error) {
	transport := map[string]string{
		"method":     "websocket",
		"session_id": sessionId,
	}

	condition := map[string]string{
		"broadcaster_user_id": broadcasterUserId,
	}

	body := map[string]interface{}{
		"type":      eventType,
		"version":   "1",
		"condition": condition,
		"transport": transport,
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal subscription body: %w", err)
	}

	req, err := http.NewRequest("POST", c.BaseURL+"/helix/eventsub/subscriptions", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create eventsub request: %w", err)
	}

	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.doRequest(req)
	if err != nil {
		return "", fmt.Errorf("eventsub subscription request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// Subscription already exists, which is fine
		return "", nil
	}

	if resp.StatusCode != http.StatusAccepted {
		var errResp map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return "", fmt.Errorf("failed to create subscription (status %d): %v", resp.StatusCode, errResp)
	}

	var subResp eventSubResponse
	if err := json.NewDecoder(resp.Body).Decode(&subResp); err != nil {
		return "", fmt.Errorf("failed to decode eventsub response: %w", err)
	}

	if len(subResp.Data) == 0 {
		return "", fmt.Errorf("eventsub response contained no subscription data")
	}

	return subResp.Data[0].ID, nil
}

// EventSubSubscription represents an existing EventSub subscription.
type EventSubSubscription struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Type      string            `json:"type"`
	Condition map[string]string `json:"condition"`
	Transport struct {
		Method    string `json:"method"`
		SessionID string `json:"session_id"`
	} `json:"transport"`
}

type eventSubSubscriptionsResponse struct {
	Data       []EventSubSubscription `json:"data"`
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
}

// GetEventSubSubscriptions lists EventSub subscriptions of a given type, following pagination.
func (c *Client) GetEventSubSubscriptions(eventType string) ([]EventSubSubscription, error) {
	var subs []EventSubSubscription
	cursor := ""

	for {
		q := url.Values{}
		q.Set("type", eventType)
		if cursor != "" {
			q.Set("after", cursor)
		}

		req, err := http.NewRequest("GET", c.BaseURL+"/helix/eventsub/subscriptions?"+q.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create eventsub list request: %w", err)
		}

		c.setHeaders(req)

		resp, err := c.doRequest(req)
		if err != nil {
			return nil, fmt.Errorf("eventsub list request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("eventsub list api returned status %d", resp.StatusCode)
		}

		var listResp eventSubSubscriptionsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode eventsub list response: %w", err)
		}
		resp.Body.Close()

		subs = append(subs, listResp.Data...)

		if listResp.Pagination.Cursor == "" {
			break
		}
		cursor = listResp.Pagination.Cursor
	}

	return subs, nil
}

// UnsubscribeEventSub deletes an active EventSub subscription by ID.
func (c *Client) UnsubscribeEventSub(subscriptionId string) error {
	req, err := http.NewRequest("DELETE", c.BaseURL+"/helix/eventsub/subscriptions?id="+subscriptionId, nil)
	if err != nil {
		return fmt.Errorf("failed to create unsubscribe request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return fmt.Errorf("eventsub unsubscribe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}

	return fmt.Errorf("failed to delete subscription (status %d)", resp.StatusCode)
}

func (c *Client) setHeaders(req *http.Request) {
	c.mu.Lock()
	accessToken := c.accessToken
	clientId := c.clientId
	c.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Client-Id", clientId)
}

func (c *Client) doRequest(req *http.Request) (*http.Response, error) {
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		clientId := c.clientId
		clientSecret := c.clientSecret
		refreshToken := c.refreshToken
		c.mu.Unlock()

		if clientSecret != "" && refreshToken != "" {
			resp.Body.Close() // Close previous 401 response body

			fmt.Println("[Twitch Client] 401 Unauthorized detected. Attempting token refresh...")
			newAccessToken, newRefreshToken, err := auth.RefreshAccessToken(clientId, clientSecret, refreshToken)
			if err != nil {
				return nil, fmt.Errorf("failed to refresh access token on 401: %w", err)
			}

			c.mu.Lock()
			c.accessToken = newAccessToken
			c.refreshToken = newRefreshToken
			c.mu.Unlock()

			// Re-apply headers with the new token
			c.setHeaders(req)

			// Recreate body if present
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("failed to restore request body for retry: %w", err)
				}
				req.Body = body
			}

			fmt.Println("[Twitch Client] Token refreshed successfully. Retrying request...")
			return c.httpClient.Do(req)
		}
	}

	return resp, nil
}

// Game represents a Twitch game category.
type Game struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BoxArtURL string `json:"box_art_url"`
}

type gamesResponse struct {
	Data []Game `json:"data"`
}

// GetGameByName retrieves a Game object by its exact name.
func (c *Client) GetGameByName(name string) (*Game, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/helix/games?name="+url.QueryEscape(name), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create games request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("games request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("games api returned status %d", resp.StatusCode)
	}

	var gamesResp gamesResponse
	if err := json.NewDecoder(resp.Body).Decode(&gamesResp); err != nil {
		return nil, fmt.Errorf("failed to decode games response: %w", err)
	}

	if len(gamesResp.Data) == 0 {
		return nil, fmt.Errorf("game '%s' not found", name)
	}

	return &gamesResp.Data[0], nil
}

// GetTopStreamsByGame retrieves the current top live streams for a game ID.
func (c *Client) GetTopStreamsByGame(gameID string) ([]Stream, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/helix/streams?game_id="+gameID+"&first=50", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create top streams request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("top streams request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("top streams api returned status %d", resp.StatusCode)
	}

	var streamsResp streamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&streamsResp); err != nil {
		return nil, fmt.Errorf("failed to decode top streams response: %w", err)
	}

	return streamsResp.Data, nil
}

// GetStreamByUser retrieves stream status for a given channel name if it is currently live.
func (c *Client) GetStreamByUser(username string) (*Stream, error) {
	req, err := http.NewRequest("GET", c.BaseURL+"/helix/streams?user_login="+username, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create user stream request: %w", err)
	}

	c.setHeaders(req)

	resp, err := c.doRequest(req)
	if err != nil {
		return nil, fmt.Errorf("user stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user stream api returned status %d", resp.StatusCode)
	}

	var streamsResp streamsResponse
	if err := json.NewDecoder(resp.Body).Decode(&streamsResp); err != nil {
		return nil, fmt.Errorf("failed to decode user stream response: %w", err)
	}

	if len(streamsResp.Data) == 0 {
		return nil, nil // Not live
	}

	return &streamsResp.Data[0], nil
}
