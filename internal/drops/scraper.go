package drops

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Campaign represents an active Twitch drops campaign.
type Campaign struct {
	Name         string   `json:"name"`
	Dates        string   `json:"dates"`
	EndTS        int64    `json:"end_ts"` // Unix timestamp in milliseconds
	Channels     []string `json:"channels"` // ["all"] or list of live/monitored channels
	AutoWatching bool     `json:"auto_watching"`
	WatchTime    int      `json:"watch_time"` // in seconds
}

var (
	nameRegex        = regexp.MustCompile(`<span class="cb-name">([^<]+)</span>`)
	datesRegex       = regexp.MustCompile(`<span class="cb-dates">([^<]+)</span>`)
	timerRegex       = regexp.MustCompile(`data-end-ts="(\d+)"`)
	channelLinkRegex = regexp.MustCompile(`class="channel-link"[^>]*>([^<]+)</a>`)
)

// Slugify converts a game name into the twitchdrops.app URL slug format.
func Slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			lastDash = false
		} else if r == ' ' || r == '-' || r == ':' || r == '_' {
			if !lastDash && sb.Len() > 0 {
				sb.WriteRune('-')
				lastDash = true
			}
		}
	}
	res := sb.String()
	res = strings.Trim(res, "-")
	return res
}

// GetActiveDrops scrapes twitchdrops.app for active campaigns for the specified game.
func GetActiveDrops(gameName string) ([]Campaign, error) {
	slug := Slugify(gameName)
	if slug == "" {
		return nil, fmt.Errorf("invalid game name: %s", gameName)
	}

	url := fmt.Sprintf("https://twitchdrops.app/game/%s", slug)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch drops page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return []Campaign{}, nil // No page/no drops found for this game
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received status code %d from twitchdrops.app", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return parseCampaignsHTML(string(bodyBytes)), nil
}

// parseCampaignsHTML parses the raw HTML from twitchdrops.app and extracts active campaigns.
func parseCampaignsHTML(html string) []Campaign {
	// Truncate at past campaigns or past drops to avoid parsing expired campaigns
	if idx := strings.Index(html, "Past Campaigns"); idx != -1 {
		html = html[:idx]
	}
	if idx := strings.Index(html, "Past Drops"); idx != -1 {
		html = html[:idx]
	}

	// Split by campaign banner start prefix (allowing modifiers like "ending-urgent")
	chunks := strings.Split(html, `<div class="campaign-banner`)
	if len(chunks) <= 1 {
		return []Campaign{}
	}

	var campaigns []Campaign
	// Skip the first chunk as it contains everything before the first campaign banner
	for _, chunk := range chunks[1:] {
		// Find where the tag attributes end
		tagEnd := strings.Index(chunk, ">")
		if tagEnd == -1 {
			continue
		}

		tagAttrs := chunk[:tagEnd]
		// Skip expired campaigns
		if strings.Contains(tagAttrs, "expired") {
			continue
		}

		campaign := Campaign{}

		// Parse Name
		if match := nameRegex.FindStringSubmatch(chunk); len(match) > 1 {
			campaign.Name = strings.TrimSpace(match[1])
			// Decode HTML entities briefly if any (e.g. &#39; -> ')
			campaign.Name = htmlDecode(campaign.Name)
		}

		// Parse Dates
		if match := datesRegex.FindStringSubmatch(chunk); len(match) > 1 {
			campaign.Dates = strings.TrimSpace(match[1])
		}

		// Parse EndTS
		if match := timerRegex.FindStringSubmatch(chunk); len(match) > 1 {
			if ts, err := strconv.ParseInt(match[1], 10, 64); err == nil {
				campaign.EndTS = ts
			}
		}

		// Parse Channels
		if strings.Contains(chunk, "All Channels") {
			campaign.Channels = []string{"all"}
		} else {
			matches := channelLinkRegex.FindAllStringSubmatch(chunk, -1)
			for _, m := range matches {
				if len(m) > 1 {
					campaign.Channels = append(campaign.Channels, strings.TrimSpace(m[1]))
				}
			}
			if len(campaign.Channels) == 0 {
				// Fallback if none found but not explicitly "All Channels" either
				campaign.Channels = []string{"all"}
			}
		}

		if campaign.Name != "" {
			campaigns = append(campaigns, campaign)
		}
	}

	return campaigns
}

func htmlDecode(s string) string {
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return s
}
