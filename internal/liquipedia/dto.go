package liquipedia

import (
	"encoding/json"
	"strings"
)

// MatchResponse is the envelope returned by GET /api/v3/match.
type MatchResponse struct {
	Result []Match  `json:"result"`
	Error  []string `json:"error"`
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
	Opponents       []Opponent `json:"match2opponents"`
	Games           []Game     `json:"match2games"`
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

// Heroes returns the hero picks of the game-opponent's primary player.
func (g GameOpponent) Heroes() []string {
	if len(g.Players) > 0 {
		return g.Players[0].Heroes
	}
	return nil
}

// Faction returns the race the game-opponent's primary player used on this map
// (can differ per game when a player goes Random).
func (g GameOpponent) Faction() string {
	if len(g.Players) > 0 {
		return g.Players[0].Faction
	}
	return ""
}

// firstPlayer returns the opponent's primary player (1v1 case), or a zero value.
func (o Opponent) firstPlayer() Player {
	if len(o.Players) > 0 {
		return o.Players[0]
	}
	return Player{}
}

// Faction returns the single-letter race code of the opponent's primary player.
func (o Opponent) Faction() string { return o.firstPlayer().ExtraData.Faction }

// Flag returns the country name of the opponent's primary player.
func (o Opponent) Flag() string { return o.firstPlayer().Flag }

// DisplayName returns the best human label for the opponent.
func (o Opponent) Display() string {
	if p := o.firstPlayer(); p.DisplayName != "" {
		return p.DisplayName
	}
	if o.Name != "" {
		return o.Name
	}
	return "TBD"
}
