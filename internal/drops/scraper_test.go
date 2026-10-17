package drops

import (
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Rust", "rust"},
		{"Overwatch 2", "overwatch-2"},
		{"World of Tanks", "world-of-tanks"},
		{"Rivals of Aether II", "rivals-of-aether-ii"},
		{"  Dead by Daylight  ", "dead-by-daylight"},
		{"Tom Clancy's Ghost Recon: Wildlands", "tom-clancys-ghost-recon-wildlands"},
	}

	for _, tt := range tests {
		result := Slugify(tt.input)
		if result != tt.expected {
			t.Errorf("Slugify(%q) = %q; want %q", tt.input, result, tt.expected)
		}
	}
}

func TestParseCampaignsHTML(t *testing.T) {
	mockHTML := `
<!DOCTYPE html>
<html>
<body>
  <div class="campaign-banner">
    <div class="cb-top">
      <div class="cb-title-row">
        <span class="cb-name">Rust Charity &#39;26 Turret</span>
      </div>
    </div>
    <div class="cb-meta">
      <span class="cb-dates">Jul 29 &#151; in 16 days</span>
      <span class="cb-timer" data-end-ts="1787752799999"></span>
    </div>
    <div class="cb-channels"><strong>Channels:</strong> <span class="badge">All Channels</span></div>
  </div>

  <div class="campaign-banner">
    <div class="cb-top">
      <div class="cb-title-row">
        <span class="cb-name">Exclusive Streamer Drop</span>
      </div>
    </div>
    <div class="cb-meta">
      <span class="cb-dates">Aug 10 &#151; in 2 days</span>
      <span class="cb-timer" data-end-ts="1786523400000"></span>
    </div>
    <div class="cb-channels">
      <strong>Channels:</strong> 
      <a href="https://twitch.tv/shroud" class="channel-link">shroud</a>
      <a href="https://twitch.tv/lirik" class="channel-link">lirik</a>
    </div>
  </div>

  <h2>Past Campaigns</h2>
  <div class="campaign-banner expired campaign-expired">
    <span class="cb-name">Expired Drop Campaign</span>
  </div>
</body>
</html>
`

	campaigns := parseCampaignsHTML(mockHTML)

	if len(campaigns) != 2 {
		t.Fatalf("Expected 2 campaigns parsed, got %d", len(campaigns))
	}

	// First campaign check
	c1 := campaigns[0]
	if c1.Name != "Rust Charity '26 Turret" {
		t.Errorf("Expected first campaign name 'Rust Charity '26 Turret', got %q", c1.Name)
	}
	if c1.Dates != "Jul 29 — in 16 days" && c1.Dates != "Jul 29 &#151; in 16 days" {
		t.Errorf("Expected dates for first campaign, got %q", c1.Dates)
	}
	if c1.EndTS != 1787752799999 {
		t.Errorf("Expected EndTS 1787752799999, got %d", c1.EndTS)
	}
	if len(c1.Channels) != 1 || c1.Channels[0] != "all" {
		t.Errorf("Expected 'all' channels, got %v", c1.Channels)
	}

	// Second campaign check
	c2 := campaigns[1]
	if c2.Name != "Exclusive Streamer Drop" {
		t.Errorf("Expected second campaign name 'Exclusive Streamer Drop', got %q", c2.Name)
	}
	if c2.EndTS != 1786523400000 {
		t.Errorf("Expected EndTS 1786523400000, got %d", c2.EndTS)
	}
	if len(c2.Channels) != 2 || c2.Channels[0] != "shroud" || c2.Channels[1] != "lirik" {
		t.Errorf("Expected channels ['shroud', 'lirik'], got %v", c2.Channels)
	}
}
