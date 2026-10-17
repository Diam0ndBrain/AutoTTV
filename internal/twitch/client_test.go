package twitch_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"autoTtv/internal/twitch"
)

func TestGetUserID_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/users" {
			t.Errorf("expected path /helix/users, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("login") != "shroud" {
			t.Errorf("expected query parameter login=shroud, got %s", r.URL.Query().Get("login"))
		}

		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":           "10137",
					"login":        "shroud",
					"display_name": "shroud",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	userId, err := client.GetUserID("shroud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if userId != "10137" {
		t.Errorf("expected user ID 10137, got %s", userId)
	}
}

func TestGetUserID_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"data": []interface{}{},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	_, err := client.GetUserID("nonexistent_user")
	if err == nil {
		t.Fatal("expected error for nonexistent user, but got nil")
	}
}

func TestIsLive_Live(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/streams" {
			t.Errorf("expected path /helix/streams, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_id") != "10137" {
			t.Errorf("expected query parameter user_id=10137, got %s", r.URL.Query().Get("user_id"))
		}

		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":         "12345",
					"user_id":    "10137",
					"user_login": "shroud",
					"type":       "live",
					"title":      "CS:GO with friends",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	live, err := client.IsLive("10137")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !live {
		t.Error("expected stream to be live, got false")
	}
}

func TestIsLive_Offline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"data": []interface{}{},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	live, err := client.IsLive("10137")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if live {
		t.Error("expected stream to be offline, got true")
	}
}

func TestSubscribeEventSub_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/eventsub/subscriptions" {
			t.Errorf("expected path /helix/eventsub/subscriptions, got %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected method POST, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if body["type"] != "stream.online" {
			t.Errorf("expected type stream.online, got %s", body["type"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "sub-12345"},
			},
		})
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	subID, err := client.SubscribeEventSub("dummy-session-id", "stream.online", "10137")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subID != "sub-12345" {
		t.Errorf("expected subID 'sub-12345', got '%s'", subID)
	}
}

func TestUnsubscribeEventSub_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/eventsub/subscriptions" {
			t.Errorf("expected path /helix/eventsub/subscriptions, got %s", r.URL.Path)
		}
		if r.Method != "DELETE" {
			t.Errorf("expected method DELETE, got %s", r.Method)
		}
		if r.URL.Query().Get("id") != "sub-12345" {
			t.Errorf("expected query parameter id='sub-12345', got %s", r.URL.Query().Get("id"))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	err := client.UnsubscribeEventSub("sub-12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetGameByName_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/games" {
			t.Errorf("expected path /helix/games, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("name") != "Overwatch 2" {
			t.Errorf("expected query parameter name='Overwatch 2', got %s", r.URL.Query().Get("name"))
		}

		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":   "515025",
					"name": "Overwatch 2",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	game, err := client.GetGameByName("Overwatch 2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if game == nil || game.ID != "515025" {
		t.Errorf("expected game ID 515025, got %v", game)
	}
}

func TestGetTopStreamsByGame_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/streams" {
			t.Errorf("expected path /helix/streams, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("game_id") != "515025" {
			t.Errorf("expected query parameter game_id=515025, got %s", r.URL.Query().Get("game_id"))
		}

		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":         "12345",
					"user_id":    "10137",
					"user_login": "shroud",
					"type":       "live",
					"title":      "OW2 with friends",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	streams, err := client.GetTopStreamsByGame("515025")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(streams) != 1 || streams[0].UserLogin != "shroud" {
		t.Errorf("expected 1 stream with login shroud, got %v", streams)
	}
}

func TestGetStreamByUser_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/streams" {
			t.Errorf("expected path /helix/streams, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_login") != "shroud" {
			t.Errorf("expected query parameter user_login=shroud, got %s", r.URL.Query().Get("user_login"))
		}

		response := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":         "12345",
					"user_id":    "10137",
					"user_login": "shroud",
					"type":       "live",
					"title":      "OW2 with friends",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	stream, err := client.GetStreamByUser("shroud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stream == nil || stream.UserLogin != "shroud" {
		t.Errorf("expected shroud stream, got %v", stream)
	}
}

func TestGetUsersByLogin_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/users" {
			t.Errorf("expected path /helix/users, got %s", r.URL.Path)
		}
		logins := r.URL.Query()["login"]
		if len(logins) != 2 || logins[0] != "shroud" || logins[1] != "pokimane" {
			t.Errorf("expected login=[shroud pokimane], got %v", logins)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "10137", "login": "shroud", "display_name": "shroud"},
				{"id": "44455", "login": "pokimane", "display_name": "pokimane"},
			},
		})
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	users, err := client.GetUsersByLogin([]string{"shroud", "pokimane"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(users) != 2 || users[0].ID != "10137" || users[1].ID != "44455" {
		t.Errorf("unexpected users: %v", users)
	}
}

func TestGetUsersByLogin_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for empty input")
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	users, err := client.GetUsersByLogin(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if users != nil {
		t.Errorf("expected nil users, got %v", users)
	}
}

func TestGetLiveUserIDs_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/helix/streams" {
			t.Errorf("expected path /helix/streams, got %s", r.URL.Path)
		}
		ids := r.URL.Query()["user_id"]
		if len(ids) != 3 {
			t.Errorf("expected 3 user_id params, got %v", ids)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "1", "user_id": "10137", "user_login": "shroud", "type": "live"},
				{"id": "2", "user_id": "99999", "user_login": "other", "type": "live"},
			},
		})
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	live, err := client.GetLiveUserIDs([]string{"10137", "44455", "99999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !live["10137"] || !live["99999"] || live["44455"] {
		t.Errorf("unexpected live map: %v", live)
	}
}

func TestGetLiveUserIDs_Empty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for empty input")
	}))
	defer server.Close()

	client := twitch.NewClient("dummy-client-id", "dummy-secret", "dummy-token", "dummy-refresh")
	client.BaseURL = server.URL

	live, err := client.GetLiveUserIDs(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("expected empty map, got %v", live)
	}
}
