package liquipedia

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MatchResponse is the envelope returned by GET /api/v3/match.
type MatchResponse struct {
	Result []Match  `json:"result"`
	Error  []string `json:"error"`
}

// TournamentResponse is the envelope returned by GET /api/v3/tournament.
type TournamentResponse struct {
	Result []Tournament `json:"result"`
	Error  []string     `json:"error"`
}

// Tournament mirrors the LiquipediaDB tournament datapoint (requested fields).
type Tournament struct {
	Pagename           string  `json:"pagename"`
	Name               string  `json:"name"`
	PrizePool          float64 `json:"prizepool"` // normalized to USD
	ParticipantsNumber int     `json:"participantsnumber"`
	StartDate          string  `json:"startdate"`
	EndDate            string  `json:"enddate"`
	LiquipediaTier     string  `json:"liquipediatier"`
}

// Match mirrors the LiquipediaDB match2 datapoint (fields we request via `query`).
type Match struct {
	Match2ID        string     `json:"match2id"`
	PageName        string     `json:"pagename"`
	Date            string     `json:"date"`      // "2006-01-02 15:04:05" (UTC)
	DateExact       int        `json:"dateexact"` // 1 = time is precise, 0 = day-only/TBD
	Finished        int        `json:"finished"`  // 1 = completed
	Winner          string     `json:"winner"`    // "1" / "2" / "" (opponent index)
	Walkover        string     `json:"walkover"`
	ResultType      string     `json:"resulttype"` // "" normal, "np" not played, "default" walkover
	BestOf          int        `json:"bestof"`
	Tournament      string     `json:"tournament"`
	Parent          string     `json:"parent"` // wiki page slug, e.g. "Gladiator_Cup/323"
	Game            string     `json:"game"`   // "reforged" / "frozenthrone"
	Patch           string     `json:"patch"`
	Series          string     `json:"series"`
	LiquipediaTier  string     `json:"liquipediatier"`
	LiquipediaTierType string  `json:"liquipediatiertype"`
	Vod             string     `json:"vod"`
	Stream          json.RawMessage `json:"stream"`
	Links           Links      `json:"links"`
	BracketData     BracketData `json:"match2bracketdata"`
	Opponents       []Opponent `json:"match2opponents"`
	Games           []Game     `json:"match2games"`
}

// BracketData describes where a match sits in its bracket. The API returns an
// empty array when absent, so decode leniently.
type BracketData struct {
	Type           string `json:"type"` // "bracket" / "matchlist"
	BracketSection string `json:"bracketsection"` // "upper" / "lower" / "mid"
	Header         string `json:"header"` // i18n code ("!u4!2") or plain text
	Coordinates    struct {
		SemanticDepth      int `json:"semanticDepth"` // 0 = root (final)
		SemanticRoundIndex int `json:"semanticRoundIndex"`
		SectionCount       int `json:"sectionCount"` // 1 = single elimination
	} `json:"coordinates"`
}

func (b *BracketData) UnmarshalJSON(data []byte) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 || data[0] == '[' { // "[]" => no bracket data
		return nil
	}
	type alias BracketData
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*b = BracketData(a)
	return nil
}

// Stage returns a readable bracket stage ("Grand Final", "Upper Bracket
// Semifinal", "Lower Bracket Round 2", ...) or "" when unknown.
func (m Match) Stage() string {
	bd := m.BracketData
	if bd.Type != "bracket" {
		// Matchlists (groups) sometimes carry a human header; codes start with "!".
		if bd.Header != "" && !strings.HasPrefix(bd.Header, "!") {
			return bd.Header
		}
		return ""
	}
	co := bd.Coordinates
	single := co.SectionCount <= 1
	prefix := ""
	switch bd.BracketSection {
	case "upper":
		if !single {
			prefix = "Upper Bracket "
		}
	case "lower":
		prefix = "Lower Bracket "
	}
	switch co.SemanticDepth {
	case 0:
		if single {
			return "Final"
		}
		return "Grand Final"
	case 1:
		if single {
			return "Semifinal"
		}
		return prefix + "Final"
	case 2:
		if single {
			return "Quarterfinal"
		}
		return prefix + "Semifinal"
	case 3:
		if single {
			return "Round of 16"
		}
		return prefix + "Quarterfinal"
	}
	if single {
		return fmt.Sprintf("Round of %d", 1<<(co.SemanticDepth+1))
	}
	return fmt.Sprintf("%sRound %d", prefix, co.SemanticRoundIndex+1)
}

// Links holds the auxiliary links Liquipedia attaches to a match. The API
// returns an empty array (not an object) when there are none, so we decode
// leniently.
type Links struct {
	HeadToHead string `json:"headtohead"`
}

func (l *Links) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || b[0] == '[' { // "[]" => no links
		return nil
	}
	type alias Links
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*l = Links(a)
	return nil
}

// LiveEvidence reports hard signs the match is actually underway: a known
// series score or a recorded game. Needed for day-only matches (dateexact=0),
// whose placeholder time passes at midnight long before the real start.
func (m Match) LiveEvidence() bool {
	if len(m.Opponents) >= 2 && m.Opponents[0].Score >= 0 && m.Opponents[1].Score >= 0 {
		return true
	}
	for _, g := range m.Games {
		if g.Winner != "" {
			return true
		}
		for _, s := range g.Scores {
			if s != 0 {
				return true
			}
		}
	}
	return false
}

// StreamURL returns a best-effort "watch" URL from the stream field, which
// Liquipedia encodes inconsistently (object of platform to url, or list). Returns
// "" when none present.
func (m Match) StreamURL() string {
	if len(m.Stream) == 0 {
		return ""
	}
	// shape 1: {"twitch":"https://...","youtube":"..."}
	var asMap map[string]string
	if err := json.Unmarshal(m.Stream, &asMap); err == nil {
		for _, v := range asMap {
			if strings.HasPrefix(v, "http") {
				return v
			}
		}
	}
	// shape 2: [{"platform":"twitch","link":"https://..."}] or ["https://..."]
	var asList []json.RawMessage
	if err := json.Unmarshal(m.Stream, &asList); err == nil {
		for _, raw := range asList {
			var s string
			if json.Unmarshal(raw, &s) == nil && strings.HasPrefix(s, "http") {
				return s
			}
			var obj struct {
				Link string `json:"link"`
				URL  string `json:"url"`
			}
			if json.Unmarshal(raw, &obj) == nil {
				if strings.HasPrefix(obj.Link, "http") {
					return obj.Link
				}
				if strings.HasPrefix(obj.URL, "http") {
					return obj.URL
				}
			}
		}
	}
	return ""
}

// Opponent is one side of the match. For WC3 1v1 these are type "solo" with a
// single player; team modes carry multiple match2players.
type Opponent struct {
	ID       int      `json:"id"`
	Type     string   `json:"type"` // "solo" / "team" / "literal"
	Name     string   `json:"name"`
	Score    int      `json:"score"` // -1 = not played yet
	Status   string   `json:"status"`
	Players  []Player `json:"match2players"`
}

// Player is one competitor with their country flag and chosen faction (race).
type Player struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"displayname"`
	Flag        string        `json:"flag"` // full country name, e.g. "China", "Peru"
	ExtraData   PlayerExtra   `json:"extradata"`
}

type PlayerExtra struct {
	Faction    string `json:"faction"` // h/o/u/n/r (Human/Orc/Undead/NightElf/Random)
	PlayerTeam string `json:"playerteam"`
}

func (p *PlayerExtra) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || b[0] == '[' { // "[]" => no extra data
		return nil
	}
	type alias PlayerExtra
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = PlayerExtra(a)
	return nil
}

// Game is one map within the match (Bo3/Bo5 leg).
type Game struct {
	Map       string         `json:"map"`
	Scores    []int          `json:"scores"`    // [opp1, opp2] for this game; 1 = winner
	Winner    string         `json:"winner"`    // "1"/"2"
	Opponents []GameOpponent `json:"opponents"` // per-side result with hero picks
}

type GameOpponent struct {
	Score   int          `json:"score"`
	Players []GamePlayer `json:"players"`
}

type GamePlayer struct {
	Player  string   `json:"player"`
	Heroes  []string `json:"heroes"` // hero names in pick order (no levels in the API)
	Faction string   `json:"faction"`
}

// Heroes returns the hero picks of all the game-opponent's players (a single
// player for 1v1; concatenated for team modes).
func (g GameOpponent) Heroes() []string {
	var out []string
	for _, p := range g.Players {
		out = append(out, p.Heroes...)
	}
	return out
}

// Faction returns the race used on this map (can differ per game when a player
// goes Random). For team modes it is only reported when all players share it.
func (g GameOpponent) Faction() string {
	f := ""
	for _, p := range g.Players {
		if f == "" {
			f = p.Faction
		} else if p.Faction != "" && p.Faction != f {
			return ""
		}
	}
	return f
}

// Faction returns the race code of the opponent's players; for team modes only
// when all players share one.
func (o Opponent) Faction() string {
	f := ""
	for _, p := range o.Players {
		pf := p.ExtraData.Faction
		if f == "" {
			f = pf
		} else if pf != "" && pf != f {
			return ""
		}
	}
	return f
}

// Flag returns the country of the opponent's players; for team modes only when
// all players share one.
func (o Opponent) Flag() string {
	f := ""
	for _, p := range o.Players {
		if f == "" {
			f = p.Flag
		} else if p.Flag != "" && p.Flag != f {
			return ""
		}
	}
	return f
}

// Solo reports whether the opponent is a single player (1v1).
func (o Opponent) Solo() bool { return len(o.Players) == 1 }

// DisplayName returns the best human label for the opponent: the player for
// 1v1, joined player names for ad-hoc teams, or the team name.
func (o Opponent) Display() string {
	var names []string
	for _, p := range o.Players {
		n := p.DisplayName
		if n == "" {
			n = p.Name
		}
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 1 {
		return strings.Join(names, " + ")
	}
	if len(names) == 1 {
		return names[0]
	}
	if o.Name != "" {
		return o.Name
	}
	return "TBD"
}
