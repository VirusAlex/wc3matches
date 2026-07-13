package w3c

import (
	"testing"
	"time"
)

func TestNormMapKey(t *testing.T) {
	cases := map[string]string{
		"Turtle Rock v2":       "turtlerock",
		"Turtle Rock":          "turtlerock",
		"Northern Isles v1.3":  "northernisles", // via stripMapVersion first
		"Autumn Leaves v2.0":   "autumnleaves",
		"Tidewater Glades LV":  "tidewaterglades",
		"Echo Isles v2.2":      "echoisles",
		"Hammerfall":           "hammerfall",
	}
	for in, want := range cases {
		if got := NormMapKey(stripMapVersion(in)); got != want {
			t.Errorf("NormMapKey(%q) = %q, want %q", in, got, want)
		}
	}
	// Cross-source matching: the whole point.
	if NormMapKey("Turtle Rock v2") != NormMapKey("Turtle Rock") {
		t.Error("versioned and plain names must match")
	}
}

func TestStripMapVersion(t *testing.T) {
	if got := stripMapVersion("Northern Isles v1.3"); got != "Northern Isles" {
		t.Errorf("stripMapVersion = %q", got)
	}
	if got := stripMapVersion("Hammerfall"); got != "Hammerfall" {
		t.Errorf("no-version name must pass through, got %q", got)
	}
}

func TestMergeGames(t *testing.T) {
	at := func(min int) time.Time {
		return time.Date(2026, 7, 13, 12, min, 0, 0, time.UTC)
	}
	w3cGames := []*GameStats{
		{ID: "mm1", Map: "Hammerfall", StartTime: at(0)},
	}
	flo := []*GameStats{
		{ID: "flo:1", Map: "Hammerfall", StartTime: at(2)},  // same game, dedup
		{ID: "flo:2", Map: "Scrimmage", StartTime: at(25)},  // new game
		{ID: "flo:3", Map: "Hammerfall", StartTime: at(50)}, // same map, later = new
	}
	out := mergeGames(w3cGames, flo)
	if len(out) != 3 {
		t.Fatalf("mergeGames len = %d, want 3", len(out))
	}
	if out[0].ID != "mm1" || out[1].ID != "flo:2" || out[2].ID != "flo:3" {
		t.Errorf("wrong order/dedup: %v %v %v", out[0].ID, out[1].ID, out[2].ID)
	}
}

func TestLeague(t *testing.T) {
	if (PlayerStats{LeagueOrder: 0}).League() != "GM" {
		t.Error("league 0 must be GM")
	}
	if (PlayerStats{LeagueOrder: 99}).League() != "" {
		t.Error("unknown league must be empty")
	}
}
