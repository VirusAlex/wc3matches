package card

import (
	"strings"
	"testing"
	"time"

	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

func soloMatch() liquipedia.Match {
	var m liquipedia.Match
	m.Date = "2026-07-13 15:00:00"
	m.DateExact = 1
	m.BestOf = 3
	m.LiquipediaTier = "2"
	m.Tournament = "Test Cup"
	m.Game = "reforged"
	m.Opponents = []liquipedia.Opponent{
		{Score: -1, Players: []liquipedia.Player{{DisplayName: "Happy", Flag: "Russia",
			ExtraData: liquipedia.PlayerExtra{Faction: "u"}}}},
		{Score: -1, Players: []liquipedia.Player{{DisplayName: "Moon", Flag: "South Korea",
			ExtraData: liquipedia.PlayerExtra{Faction: "n"}}}},
	}
	return m
}

func TestRenderUpcoming(t *testing.T) {
	now := time.Date(2026, 7, 13, 14, 0, 0, 0, time.UTC)
	html := Render(soloMatch(), now)
	for _, want := range []string{"Upcoming", "Happy", "Moon", "Bo3", "A-Tier",
		"Test Cup", "Undead", "Night Elf", "Liquipedia"} {
		if !strings.Contains(html, want) {
			t.Errorf("upcoming card must contain %q\n%s", want, html)
		}
	}
	if strings.Contains(html, "0 : 0") {
		t.Error("unknown score must not render as 0 : 0")
	}
}

func TestRenderFinishedWithExtras(t *testing.T) {
	now := time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)
	m := soloMatch()
	m.Finished = 1
	m.Winner = "1"
	m.Opponents[0].Score = 2
	m.Opponents[1].Score = 1
	m.BracketData.Type = "bracket"
	m.BracketData.BracketSection = "upper"
	m.BracketData.Coordinates.SectionCount = 2 // depth 0 => Grand Final
	m.Games = []liquipedia.Game{{Map: "Hammerfall", Winner: "1", Scores: []int{1, 0},
		Opponents: []liquipedia.GameOpponent{
			{Players: []liquipedia.GamePlayer{{Heroes: []string{"Death Knight"}, Faction: "u"}}},
			{Players: []liquipedia.GamePlayer{{Heroes: []string{"Demon Hunter"}, Faction: "n"}}},
		}}}
	x := &Extras{
		PrizeUSD: 3663.5,
		W3C: &w3c.Enrichment{
			Season: 25,
			Stats: []*w3c.PlayerStats{
				{BattleTag: "a#1", Season: 25, MMR: 2700, Rank: 3, LeagueOrder: 0},
				{BattleTag: "b#2", Season: 24, MMR: 2500, Rank: 10, LeagueOrder: 0},
			},
			Games: []*w3c.GameStats{{ID: "flo:1", Map: "Hammerfall",
				StartTime: now.Add(-time.Hour), Duration: 19*time.Minute + 45*time.Second}},
		},
	}
	html := RenderExtras(m, now, x)
	for _, want := range []string{
		"🏆 2 : 1", "Finished", "Grand Final", "💰 $3,664",
		"W3Champions ladder", "2700", "#3 GM", "(s24)", // old-season marker
		"⏱ 19:45", "Death Knight", "Hammerfall",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("finished card must contain %q\n%s", want, html)
		}
	}
}

func TestRenderLiveFloGame(t *testing.T) {
	now := time.Date(2026, 7, 13, 15, 30, 0, 0, time.UTC)
	m := soloMatch()
	m.Opponents[0].Score = 0
	m.Opponents[1].Score = 0
	x := &Extras{W3C: &w3c.Enrichment{
		Games: []*w3c.GameStats{{ID: "flo:2", Map: "Scrimmage", Live: true,
			StartTime: now.Add(-12 * time.Minute),
			Players: [2]w3c.GamePlayerStats{{Race: "Undead"}, {Race: "Night Elf"}}}},
	}}
	html := RenderExtras(m, now, x)
	for _, want := range []string{"LIVE", "Scrimmage", "in progress", "⏱ 12 min"} {
		if !strings.Contains(html, want) {
			t.Errorf("live card must contain %q\n%s", want, html)
		}
	}
	if strings.Contains(html, "Heroes") {
		t.Error("FLO-only game has no hero data; the column must be omitted")
	}
}

func TestStartedFloatingDate(t *testing.T) {
	now := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	m := soloMatch()
	m.Date = "2026-07-13 00:00:00"
	m.DateExact = 0
	if started(m, now) {
		t.Error("floating-date match without evidence must not be LIVE")
	}
	m.Opponents[0].Score = 0
	m.Opponents[1].Score = 0
	if !started(m, now) {
		t.Error("floating-date match with a known score is LIVE")
	}
}

func TestMatchTimeFloatingDate(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	m := soloMatch()
	m.Date = "2026-07-13 00:00:00"
	m.DateExact = 0

	// Upcoming, no evidence: the time is genuinely unknown.
	if got := matchTimeUTC(m, now, time.Time{}); got != "Jul 13 (time TBD)" {
		t.Errorf("upcoming floating = %q", got)
	}
	// Finished: "TBD" is nonsense, show just the date.
	m.Finished = 1
	if got := matchTimeUTC(m, now, time.Time{}); got != "Jul 13" {
		t.Errorf("finished floating = %q", got)
	}
	// With a known first game, show its real start time.
	first := time.Date(2026, 7, 13, 9, 4, 0, 0, time.UTC)
	if got := matchTimeUTC(m, now, first); got != "Jul 13, 09:04 UTC" {
		t.Errorf("floating with game start = %q", got)
	}
}

func TestFormatUSD(t *testing.T) {
	cases := map[float64]string{
		3663.5:  "$3,664",
		500:     "$500",
		2304.34: "$2,304",
		1000000: "$1,000,000",
	}
	for in, want := range cases {
		if got := FormatUSD(in); got != want {
			t.Errorf("FormatUSD(%v) = %q, want %q", in, got, want)
		}
	}
}
