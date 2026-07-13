package w3c

// FLO (w3flo.com) is the game-hosting infrastructure behind W3Champions.
// Unlike the website API, its public GraphQL endpoint also lists tournament
// games (played in FLO lobbies, not matchmaking), with real battletags. It
// only keeps a short window of recent games, so it is useful strictly for
// matches happening right now: current map, per-game races, start time and,
// once a game ends, its duration. No replay-level data (heroes/units) here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	floStatsURL = "https://stats.w3flo.com/"
	floListTTL  = time.Minute
	// floStickyFor: on refresh failure, how long a previously fetched list is
	// still trusted (the window is live data, stale is quickly useless).
	floStickyFor = 10 * time.Minute
)

const floGamesQuery = `{ games { id mapName startedAt endedAt players { name race } } }`

type floGame struct {
	ID        int
	Map       string
	StartedAt time.Time
	EndedAt   time.Time // zero = still running
	Players   map[string]string // battletag -> race name
}

// floGamesFor returns games where both tags play, oldest first, converted to
// GameStats (no heroes/units; Live set for running games). Results accumulate
// across calls: the FLO window only spans the last ~400 games (~1.5h), so in
// a long series the early maps rotate out of it before the series ends, and
// the card must not lose their durations on the next edit.
func (c *Client) floGamesFor(ctx context.Context, tag1, tag2 string, since time.Time) []*GameStats {
	list := c.floList(ctx)
	key := tag1 + "|" + tag2

	c.mu.Lock()
	seen := c.floSeen[key]
	if seen == nil {
		seen = map[string]*GameStats{}
		c.floSeen[key] = seen
	}
	inWindow := map[string]bool{}
	for _, g := range list {
		r1, ok1 := g.Players[tag1]
		r2, ok2 := g.Players[tag2]
		if !ok1 || !ok2 {
			continue
		}
		gs := &GameStats{
			ID:        fmt.Sprintf("flo:%d", g.ID),
			Map:       g.Map,
			StartTime: g.StartedAt,
			Live:      g.EndedAt.IsZero(),
		}
		if !g.EndedAt.IsZero() {
			gs.Duration = g.EndedAt.Sub(g.StartedAt)
		}
		gs.Players[0] = GamePlayerStats{BattleTag: tag1, Race: floRaceName(r1)}
		gs.Players[1] = GamePlayerStats{BattleTag: tag2, Race: floRaceName(r2)}
		seen[gs.ID] = gs // finished games are immutable; live ones get updated
		inWindow[gs.ID] = true
	}
	var out []*GameStats
	for id, gs := range seen {
		// A game that was live and then vanished from the window ended at an
		// unknown time: stop calling it live, keep it without a duration.
		if gs.Live && len(list) > 0 && !inWindow[id] {
			gs.Live = false
		}
		if time.Since(gs.StartTime) > 36*time.Hour {
			delete(seen, id) // prune long-gone games
			continue
		}
		if !gs.StartTime.Before(since) {
			out = append(out, gs)
		}
	}
	c.mu.Unlock()

	for i := 1; i < len(out); i++ { // oldest first (tiny slice)
		for j := i; j > 0 && out[j].StartTime.Before(out[j-1].StartTime); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// floList returns the cached-or-fresh FLO game window. Never errors: on
// failure the previous list is served while it is recent enough.
func (c *Client) floList(ctx context.Context) []floGame {
	c.mu.Lock()
	list, at := c.flo, c.floAt
	c.mu.Unlock()
	if time.Since(at) < floListTTL {
		return list
	}
	fresh, err := c.fetchFloList(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("w3c: flo game list fetch failed")
		if time.Since(at) < floStickyFor {
			return list
		}
		return nil
	}
	c.mu.Lock()
	c.flo, c.floAt = fresh, time.Now()
	c.mu.Unlock()
	return fresh
}

func (c *Client) fetchFloList(ctx context.Context) ([]floGame, error) {
	body, err := json.Marshal(map[string]string{"query": floGamesQuery})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			Games []struct {
				ID        int    `json:"id"`
				MapName   string `json:"mapName"`
				StartedAt string `json:"startedAt"`
				EndedAt   string `json:"endedAt"`
				Players   []struct {
					Name string `json:"name"`
					Race string `json:"race"`
				} `json:"players"`
			} `json:"games"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.doJSON(ctx, http.MethodPost, floStatsURL, body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		return nil, fmt.Errorf("flo: %s", payload.Errors[0].Message)
	}
	out := make([]floGame, 0, len(payload.Data.Games))
	for _, g := range payload.Data.Games {
		started, err := time.Parse(time.RFC3339, g.StartedAt)
		if err != nil {
			continue
		}
		fg := floGame{
			ID:        g.ID,
			Map:       stripMapVersion(g.MapName),
			StartedAt: started,
			Players:   make(map[string]string, len(g.Players)),
		}
		if t, err := time.Parse(time.RFC3339, g.EndedAt); err == nil {
			fg.EndedAt = t
		}
		for _, p := range g.Players {
			fg.Players[p.Name] = p.Race
		}
		out = append(out, fg)
	}
	return out, nil
}

// mergeGames appends FLO games that have no counterpart among the (richer)
// matchmaking games: same normalized map with a start time within 10 minutes
// counts as the same game. Result is oldest first.
func mergeGames(w3cGames, floGames []*GameStats) []*GameStats {
	out := w3cGames
	for _, fg := range floGames {
		dup := false
		for _, wg := range w3cGames {
			if NormMapKey(wg.Map) == NormMapKey(fg.Map) &&
				!wg.StartTime.IsZero() && !fg.StartTime.IsZero() &&
				absDuration(wg.StartTime.Sub(fg.StartTime)) < 10*time.Minute {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, fg)
		}
	}
	for i := 1; i < len(out); i++ { // insertion sort by start time (tiny slice)
		for j := i; j > 0 && out[j].StartTime.Before(out[j-1].StartTime); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// stripMapVersion drops a trailing version marker from a FLO map name:
// "Northern Isles v1.3" -> "Northern Isles".
var mapVersionRe = regexp.MustCompile(`(?i)\s+v[\d.]+$`)

func stripMapVersion(s string) string {
	return strings.TrimSpace(mapVersionRe.ReplaceAllString(s, ""))
}

// NormMapKey normalizes a map name for cross-source matching (Liquipedia,
// W3C matchmaking and FLO all format names differently).
var normMapVerRe = regexp.MustCompile(`v\d+$`)

func NormMapKey(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	out := normMapVerRe.ReplaceAllString(sb.String(), "")
	return strings.TrimSuffix(out, "lv")
}

func floRaceName(r string) string {
	switch r {
	case "HUMAN":
		return "Human"
	case "ORC":
		return "Orc"
	case "NIGHT_ELF":
		return "Night Elf"
	case "UNDEAD":
		return "Undead"
	case "RANDOM":
		return "Random"
	}
	return ""
}
