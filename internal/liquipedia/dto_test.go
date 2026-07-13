package liquipedia

import (
	"encoding/json"
	"testing"
)

func TestStage(t *testing.T) {
	mk := func(typ, section string, depth, roundIdx, sections int) Match {
		var m Match
		m.BracketData.Type = typ
		m.BracketData.BracketSection = section
		m.BracketData.Coordinates.SemanticDepth = depth
		m.BracketData.Coordinates.SemanticRoundIndex = roundIdx
		m.BracketData.Coordinates.SectionCount = sections
		return m
	}
	cases := []struct {
		name string
		m    Match
		want string
	}{
		{"double elim grand final", mk("bracket", "upper", 0, 3, 2), "Grand Final"},
		{"upper final", mk("bracket", "upper", 1, 2, 2), "Upper Bracket Final"},
		{"upper semi", mk("bracket", "upper", 2, 1, 2), "Upper Bracket Semifinal"},
		{"upper quarter", mk("bracket", "upper", 3, 0, 2), "Upper Bracket Quarterfinal"},
		{"lower round 1", mk("bracket", "lower", 4, 0, 2), "Lower Bracket Round 1"},
		{"lower final", mk("bracket", "lower", 1, 3, 2), "Lower Bracket Final"},
		{"single elim final", mk("bracket", "upper", 0, 2, 1), "Final"},
		{"single elim semi", mk("bracket", "upper", 1, 1, 1), "Semifinal"},
		{"single elim ro16", mk("bracket", "upper", 3, 0, 1), "Round of 16"},
		{"single elim ro32", mk("bracket", "upper", 4, 0, 1), "Round of 32"},
		{"matchlist with text header", func() Match {
			m := mk("matchlist", "", 0, 0, 0)
			m.BracketData.Header = "Group A"
			return m
		}(), "Group A"},
		{"matchlist with code header", func() Match {
			m := mk("matchlist", "", 0, 0, 0)
			m.BracketData.Header = "!u4!2"
			return m
		}(), ""},
		{"no bracket data", Match{}, ""},
	}
	for _, c := range cases {
		if got := c.m.Stage(); got != c.want {
			t.Errorf("%s: Stage() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLiveEvidence(t *testing.T) {
	upcoming := Match{Opponents: []Opponent{{Score: -1}, {Score: -1}}}
	if upcoming.LiveEvidence() {
		t.Error("upcoming match (scores -1) must not count as live")
	}
	live := Match{Opponents: []Opponent{{Score: 0}, {Score: 0}}}
	if !live.LiveEvidence() {
		t.Error("known 0:0 score is live evidence")
	}
	withGame := Match{
		Opponents: []Opponent{{Score: -1}, {Score: -1}},
		Games:     []Game{{Winner: "1", Scores: []int{1, 0}}},
	}
	if !withGame.LiveEvidence() {
		t.Error("a recorded game is live evidence")
	}
}

func TestDisplayTeam(t *testing.T) {
	duo := Opponent{Players: []Player{{DisplayName: "Happy"}, {DisplayName: "Moon"}}}
	if got := duo.Display(); got != "Happy + Moon" {
		t.Errorf("duo Display() = %q", got)
	}
	solo := Opponent{Players: []Player{{DisplayName: "Lyn"}}}
	if got := solo.Display(); got != "Lyn" {
		t.Errorf("solo Display() = %q", got)
	}
	if solo.Solo() != true || duo.Solo() != false {
		t.Error("Solo() misreports")
	}
	empty := Opponent{}
	if got := empty.Display(); got != "TBD" {
		t.Errorf("empty Display() = %q", got)
	}
}

func TestLenientUnmarshal(t *testing.T) {
	// The API sends [] instead of {} for empty links / extradata / bracketdata.
	raw := `{"match2id":"X","links":[],"match2bracketdata":[],
		"match2opponents":[{"match2players":[{"extradata":[]}]}]}`
	var m Match
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Match2ID != "X" || m.Stage() != "" {
		t.Errorf("unexpected decode result: %+v", m)
	}
}
