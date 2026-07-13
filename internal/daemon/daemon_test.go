package daemon

import (
	"testing"
	"time"

	"github.com/virusalex/wc3-tracker/internal/liquipedia"
)

func TestShouldPost(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	mk := func(date string, exact, finished int, scores ...int) liquipedia.Match {
		m := liquipedia.Match{Date: date, DateExact: exact, Finished: finished}
		s1, s2 := -1, -1
		if len(scores) == 2 {
			s1, s2 = scores[0], scores[1]
		}
		m.Opponents = []liquipedia.Opponent{{Score: s1}, {Score: s2}}
		return m
	}
	cases := []struct {
		name string
		m    liquipedia.Match
		want bool
	}{
		{"finished never posts", mk("2026-07-13 11:00:00", 1, 1), false},
		{"exact started", mk("2026-07-13 11:00:00", 1, 0), true},
		{"exact within window", mk("2026-07-13 12:20:00", 1, 0), true},
		{"exact too far ahead", mk("2026-07-13 14:00:00", 1, 0), false},
		// The core false-LIVE bug: day-only date passed at midnight but the
		// match has not actually started.
		{"floating date passed, no evidence", mk("2026-07-13 00:00:00", 0, 0), false},
		{"floating date passed, live score", mk("2026-07-13 00:00:00", 0, 0, 0, 0), true},
		{"floating future date", mk("2026-07-14 00:00:00", 0, 0), false},
	}
	for _, c := range cases {
		if got := shouldPost(c.m, now, 30); got != c.want {
			t.Errorf("%s: shouldPost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInteresting(t *testing.T) {
	favs := map[string]struct{}{"happy": {}}
	solo := func(name, tier string) liquipedia.Match {
		return liquipedia.Match{
			LiquipediaTier: tier,
			Opponents: []liquipedia.Opponent{
				{Players: []liquipedia.Player{{DisplayName: name}}},
				{Players: []liquipedia.Player{{DisplayName: "Someone"}}},
			},
		}
	}
	if !interesting(solo("Nobody", "1"), favs, 2) {
		t.Error("S-tier must be interesting")
	}
	if interesting(solo("Nobody", "3"), favs, 2) {
		t.Error("B-tier without favorites must not be interesting")
	}
	if !interesting(solo("Happy", "4"), favs, 2) {
		t.Error("favorite player must be interesting at any tier")
	}
	tbd := liquipedia.Match{LiquipediaTier: "1",
		Opponents: []liquipedia.Opponent{{}, {Players: []liquipedia.Player{{DisplayName: "X"}}}}}
	if interesting(tbd, favs, 2) {
		t.Error("TBD opponent must not be interesting")
	}
}

func TestAnchors(t *testing.T) {
	ref := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if got := lastUTCAnchor(ref, 7); got.Hour() != 7 || got.Day() != 13 {
		t.Errorf("lastUTCAnchor after 07:00 = %v", got)
	}
	early := time.Date(2026, 7, 13, 5, 0, 0, 0, time.UTC)
	if got := lastUTCAnchor(early, 7); got.Day() != 12 {
		t.Errorf("lastUTCAnchor before 07:00 must be yesterday, got %v", got)
	}
	if got := nextAtUTC(ref, 7, 0); got.Day() != 14 {
		t.Errorf("nextAtUTC after 07:00 must be tomorrow, got %v", got)
	}
}

func TestTournamentPage(t *testing.T) {
	m := liquipedia.Match{PageName: "A/B", Parent: "C/D"}
	if tournamentPage(m) != "A/B" {
		t.Error("pagename must win")
	}
	m.PageName = ""
	if tournamentPage(m) != "C/D" {
		t.Error("parent is the fallback")
	}
}
