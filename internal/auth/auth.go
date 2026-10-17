package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	serviceName         = "autottv"
	keyClientId         = "client_id"
	keyClientSecret     = "client_secret"
	keyRefreshToken     = "refresh_token"
	keyMonitoredChannel = "monitored_channel"
)

// TokenResponse represents the JSON response from Twitch's token endpoint.
type TokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

// StoredConfig represents the schema for the local secure config file.
type StoredConfig struct {
	ClientID         string `json:"client_id,omitempty"`
	ClientSecret     string `json:"client_secret,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	MonitoredChannel  string   `json:"monitored_channel,omitempty"`
	MonitoredChannels []string `json:"monitored_channels,omitempty"`
	GameName         string `json:"game_name,omitempty"`
	DropsActive      bool   `json:"drops_active,omitempty"`
}

// Settings holds non-credential application preferences persisted in the local config.
type Settings struct {
	GameName    string
	DropsActive bool
}

// GetSettings retrieves the persisted application settings from the local config.
func GetSettings() (Settings, error) {
	cfg, err := readConfig()
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		GameName:    cfg.GameName,
		DropsActive: cfg.DropsActive,
	}, nil
}

// SaveSettings persists application settings to the local config, leaving credentials untouched.
func SaveSettings(s Settings) error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	cfg.GameName = s.GameName
	cfg.DropsActive = s.DropsActive
	return writeConfig(cfg)
}

func getConfigFile() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(configDir, "autoTtv")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "credentials.json"), nil
}

func readConfig() (*StoredConfig, error) {
	path, err := getConfigFile()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &StoredConfig{}, nil
		}
		return nil, err
	}

	var cfg StoredConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func writeConfig(cfg *StoredConfig) error {
	path, err := getConfigFile()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// GetCredentials retrieves stored client ID, client secret, and refresh token from the local config, migrating from keyring if needed.
func GetCredentials() (string, string, string, error) {
	cfg, err := readConfig()
	if err != nil {
		return "", "", "", err
	}

	// If the config file is brand new, check if we can migrate from legacy OS keyring
	if cfg.ClientID == "" && cfg.ClientSecret == "" && cfg.RefreshToken == "" {
		legacyCID, _ := keyring.Get(serviceName, keyClientId)
		legacyCSEC, _ := keyring.Get(serviceName, keyClientSecret)
		legacyRToken, _ := keyring.Get(serviceName, keyRefreshToken)

		if legacyCID != "" || legacyCSEC != "" || legacyRToken != "" {
			cfg.ClientID = legacyCID
			cfg.ClientSecret = legacyCSEC
			cfg.RefreshToken = legacyRToken

			if legacyChan, err := keyring.Get(serviceName, keyMonitoredChannel); err == nil && legacyChan != "" {
				cfg.MonitoredChannel = legacyChan
			}

			// Save migrated credentials to local file
			_ = writeConfig(cfg)

			// Attempt to clean up legacy keys (ignore errors as binary signature shifts might deny write/delete)
			_ = keyring.Delete(serviceName, keyClientId)
			_ = keyring.Delete(serviceName, keyClientSecret)
			_ = keyring.Delete(serviceName, keyRefreshToken)
			_ = keyring.Delete(serviceName, keyMonitoredChannel)
		}
	}

	return cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken, nil
}

// SaveCredentials stores the client ID, client secret, and refresh token securely in the local config file.
func SaveCredentials(clientId, clientSecret, refreshToken string) error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}

	if clientId != "" {
		cfg.ClientID = clientId
	}
	if clientSecret != "" {
		cfg.ClientSecret = clientSecret
	}
	if refreshToken != "" {
		cfg.RefreshToken = refreshToken
	}

	return writeConfig(cfg)
}

// ClearCredentials wipes all stored credentials from local config and attempts to delete old OS keyring keys.
func ClearCredentials() error {
	cfg, err := readConfig()
	if err == nil {
		cfg.ClientID = ""
		cfg.ClientSecret = ""
		cfg.RefreshToken = ""
		cfg.MonitoredChannel = ""
		cfg.MonitoredChannels = nil
		_ = writeConfig(cfg)
	}

	// Attempt to wipe legacy keys from OS keyring to avoid any lingering credentials
	_ = keyring.Delete(serviceName, keyClientId)
	_ = keyring.Delete(serviceName, keyClientSecret)
	_ = keyring.Delete(serviceName, keyRefreshToken)
	_ = keyring.Delete(serviceName, keyMonitoredChannel)

	return nil
}

// GetMonitoredChannels retrieves the stored list of Twitch channel logins to monitor,
// migrating the legacy single monitored_channel field if present.
func GetMonitoredChannels() ([]string, error) {
	cfg, err := readConfig()
	if err != nil {
		return nil, err
	}
	if len(cfg.MonitoredChannels) == 0 && cfg.MonitoredChannel != "" {
		cfg.MonitoredChannels = []string{cfg.MonitoredChannel}
		cfg.MonitoredChannel = ""
		if err := writeConfig(cfg); err != nil {
			return nil, err
		}
	}
	return cfg.MonitoredChannels, nil
}

// SaveMonitoredChannels stores the list of Twitch channel logins to monitor in the local config file.
func SaveMonitoredChannels(channels []string) error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	cfg.MonitoredChannels = channels
	cfg.MonitoredChannel = ""
	return writeConfig(cfg)
}

// RefreshAccessToken takes a refresh token and uses the Twitch API to obtain a new Access Token.
func RefreshAccessToken(clientId, clientSecret, refreshToken string) (string, string, error) {
	if clientId == "" || clientSecret == "" || refreshToken == "" {
		return "", "", errors.New("missing client ID, client secret, or refresh token for refresh")
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientId)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequest("POST", "https://id.twitch.tv/oauth2/token", strings.NewReader(data.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return "", "", fmt.Errorf("failed to refresh token, status %d: %v", resp.StatusCode, errResp)
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", fmt.Errorf("failed to decode token response: %w", err)
	}

	// Update the stored refresh token if a new one is returned
	if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != refreshToken {
		if err := SaveCredentials("", "", tokenResp.RefreshToken); err != nil {
			return "", "", fmt.Errorf("failed to save new refresh token: %w", err)
		}
	}

	return tokenResp.AccessToken, tokenResp.RefreshToken, nil
}

