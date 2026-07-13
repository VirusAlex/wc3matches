// Package w3c enriches match cards with data from the public W3Champions API
// (website-backend.w3champions.com):
//
//   - player ladder context (MMR, league rank) for the current season;
//   - per-game stats (duration, final hero levels, units produced/killed) when
//     the game itself was played through W3Champions matchmaking.
//
// Pro players are matched to Liquipedia via the aka data W3Champions publishes
// in its league rankings. Everything here is best-effort: any error degrades to
// "no stats" (with a sticky cache so a transient API failure does not make an
// already-shown section disappear on the next card edit).
package w3c

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"

	"github.com/virusalex/wc3-tracker/internal/liquipedia"
)

const (
	defaultBase = "https://website-backend.w3champions.com"
	gateway     = 20 // W3Champions runs a single global gateway
	gameMode1v1 = 1

	akaTTL   = 12 * time.Hour
	statsTTL = 30 * time.Minute
	// gameSlack widens the match window when looking for W3C games: tournament
	// matches often start a bit before/after the scheduled Liquipedia time.
	gameSlack = 45 * time.Minute
)

// akaLeagues: league ids scanned for the liquipedia-name -> battletag map
// (0 = Grandmaster, then Master/Diamond divisions). Pros with aka data live in
// the top leagues; scanning deeper adds nothing.
var akaLeagues = []int{0, 1, 2, 3, 4, 5, 6}

type Client struct {
	base      string
	hcs       []*http.Client // egress routes: [0] direct, then SOCKS5 proxies
	hcLabels  []string
	cachePath string // "" = no persistence

	mu         sync.Mutex
	hcPref     int       // egress route that worked last; tried first
	seasons    []int     // latest two season ids, newest first
	seasonsAt  time.Time // when the season list was fetched
	akaTags    map[string]string // normalized liquipedia name -> battletag
	akaFetched time.Time
	akaAttempt time.Time // last refresh attempt (throttles retries on failure)
	akaBusy    bool      // a background refresh is running
	akaPartial bool      // last scan skipped leagues; retry sooner than akaTTL
	stats      map[string]statsEntry    // battletag -> cached ladder stats
	games      map[string]*GameStats    // W3C match id -> immutable finished game
	h2hAt      map[string]time.Time     // "tag1|tag2" -> last H2H fetch (throttle)
	h2hGames   map[string][]*GameStats  // "tag1|tag2" -> games found so far
	flo        []floGame                // recent FLO game window
	floAt      time.Time                // when the FLO window was fetched
	floTryAt   time.Time                // last fetch attempt (failure backoff)
	ongoing    []floGame                // W3C ongoing matchmaking games (fallback)
	ongoingAt  time.Time
	ongoingTryAt time.Time
	floSeen    map[string]map[string]*GameStats // pair -> flo game id -> game;
	// accumulated across refreshes: the FLO window only spans ~1.5h, so games
	// of a long series rotate out of it before the series ends.
	sweepAt time.Time // last cache sweep (the daemon runs for months)
}

type statsEntry struct {
	s  *PlayerStats // last GOOD value; kept on refresh failure (sticky)
	at time.Time
}

func New() *Client {
	return &Client{
		base: defaultBase,
		// The league-ranking endpoints regularly take 15-30s to answer.
		hcs:      []*http.Client{{Timeout: 40 * time.Second}},
		hcLabels: []string{"direct"},
		stats:    map[string]statsEntry{},
		games:    map[string]*GameStats{},
		h2hAt:    map[string]time.Time{},
		h2hGames: map[string][]*GameStats{},
		floSeen:  map[string]map[string]*GameStats{},
	}
}

// AddProxies appends SOCKS5 egress routes. When the API throttles the direct
// IP (requests hang until timeout), the next route is tried and, once it
// works, becomes the preferred one.
func (c *Client) AddProxies(urls ...string) error {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("w3c: parse proxy url %q: %w", raw, err)
		}
		dialer, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return fmt.Errorf("w3c: proxy dialer %q: %w", raw, err)
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return fmt.Errorf("w3c: proxy %q does not support contexts", raw)
		}
		c.hcs = append(c.hcs, &http.Client{
			Timeout:   40 * time.Second,
			Transport: &http.Transport{DialContext: cd.DialContext},
		})
		c.hcLabels = append(c.hcLabels, u.Host)
	}
	return nil
}

// NewWithCache is New plus a JSON file that persists the aka map across
// restarts, so a redeploy does not rescan 14 league rankings (bursts of which
// get our IP throttled by the API).
func NewWithCache(path string) *Client {
	c := New()
	c.cachePath = path
	c.loadCache()
	return c
}

// akaCache is the on-disk shape of the persisted aka map.
type akaCache struct {
	Seasons    []int             `json:"seasons"`
	Tags       map[string]string `json:"tags"`
	FetchedAt  time.Time         `json:"fetchedAt"`
	Partial    bool              `json:"partial"`
}

func (c *Client) loadCache() {
	if c.cachePath == "" {
		return
	}
	data, err := os.ReadFile(c.cachePath)
	if err != nil {
		return // no cache yet
	}
	var cached akaCache
	if err := json.Unmarshal(data, &cached); err != nil || len(cached.Tags) == 0 {
		return
	}
	c.mu.Lock()
	c.seasons = cached.Seasons
	c.seasonsAt = cached.FetchedAt
	c.akaTags = cached.Tags
	c.akaFetched = cached.FetchedAt
	c.akaPartial = cached.Partial
	c.mu.Unlock()
	log.Info().Int("names", len(cached.Tags)).Time("fetchedAt", cached.FetchedAt).
		Bool("partial", cached.Partial).Msg("w3c: aka map loaded from cache")
}

// saveCache is called with c.mu NOT held; it snapshots and writes best-effort.
func (c *Client) saveCache() {
	if c.cachePath == "" {
		return
	}
	c.mu.Lock()
	cached := akaCache{
		Seasons:   c.seasons,
		Tags:      c.akaTags,
		FetchedAt: c.akaFetched,
		Partial:   c.akaPartial,
	}
	c.mu.Unlock()
	data, err := json.Marshal(cached)
	if err != nil {
		return
	}
	if err := os.WriteFile(c.cachePath, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("w3c: aka cache write failed")
	}
}

// Prewarm builds the aka map in the background so the first card render does
// not have to pay for the slow league scan.
func (c *Client) Prewarm() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		seasons, err := c.currentSeasons(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("w3c: prewarm season lookup failed")
			return
		}
		if err := c.ensureAka(ctx, seasons); err != nil {
			log.Warn().Err(err).Msg("w3c: prewarm aka scan failed")
			return
		}
		log.Info().Msg("w3c: aka map prewarmed")
	}()
}

// PlayerStats is the 1v1 ladder snapshot for one player (their main race row).
type PlayerStats struct {
	BattleTag   string
	Season      int
	MMR         int
	Rank        int // rank number within the league
	LeagueOrder int // 0 = Grandmaster
}

// League returns a short league label for the stats row.
func (s PlayerStats) League() string {
	names := []string{"GM", "Master", "Diamond", "Platinum", "Gold", "Silver", "Bronze", "Grass"}
	if s.LeagueOrder >= 0 && s.LeagueOrder < len(names) {
		return names[s.LeagueOrder]
	}
	return ""
}

// GameStats is one game between the two players, from W3C matchmaking
// (full replay stats) or from the FLO game window (map/races/duration only).
type GameStats struct {
	ID        string
	Map       string
	StartTime time.Time
	Duration  time.Duration
	Live      bool // still running (FLO only)
	// Players is aligned to the order the tags were requested in (opponent 1,
	// opponent 2) regardless of the in-game team order.
	Players [2]GamePlayerStats
}

type GamePlayerStats struct {
	BattleTag     string
	Won           bool
	Race          string // resolved race name ("Human", ...), "" if unknown
	Heroes        []HeroStat
	UnitsProduced int
	UnitsKilled   int
}

type HeroStat struct {
	Name  string
	Level int
}

// Enrichment is everything W3Champions knows about a Liquipedia match.
type Enrichment struct {
	Season int            // current ladder season (0 = unknown)
	Stats  []*PlayerStats // aligned to match opponents; nil = unknown player
	Games  []*GameStats   // games between the two players since match start
}

// EnrichMatch resolves both opponents of a Liquipedia match and, when both are
// known on W3Champions and the match has started, their games since the start
// time. Never returns an error: enrichment is optional by design.
func (c *Client) EnrichMatch(ctx context.Context, m liquipedia.Match) *Enrichment {
	c.sweep()
	e := &Enrichment{Stats: make([]*PlayerStats, len(m.Opponents))}
	if ss, err := c.currentSeasons(ctx); err == nil && len(ss) > 0 {
		e.Season = ss[0]
	}
	var tags [2]string
	for i, o := range m.Opponents {
		if !o.Solo() {
			continue // ladder identity is per-player; skip team opponents
		}
		names := []string{o.Players[0].Name, o.Display()}
		tag := c.tagFor(ctx, names...)
		if tag == "" {
			continue
		}
		if i < len(tags) {
			tags[i] = tag
		}
		e.Stats[i] = c.statsFor(ctx, tag) // optional: games work off tags alone
	}
	if tags[0] != "" && tags[1] != "" {
		if start, err := time.Parse("2006-01-02 15:04:05", m.Date); err == nil {
			since := start.Add(-gameSlack)
			// FLO first: it also sees tournament games (matchmaking API does
			// not) and its host stays fast when W3C throttles us.
			flo := c.floGamesFor(ctx, tags[0], tags[1], since)
			e.Games = mergeGames(c.h2hSince(ctx, tags[0], tags[1], since), flo)
			// Fallback live source: when the FLO window shows no running game
			// for an unfinished match, the W3C ongoing list may still see a
			// ladder-hosted game (redundancy for FLO outages).
			if m.Finished != 1 && !anyLive(e.Games) {
				if lg := c.ongoingLive(ctx, tags[0], tags[1]); lg != nil && !lg.StartTime.Before(since) {
					e.Games = mergeGames(e.Games, []*GameStats{lg})
				}
			}
		}
	}
	return e
}

func anyLive(games []*GameStats) bool {
	for _, g := range games {
		if g.Live {
			return true
		}
	}
	return false
}

// HasLiveGame reports whether a game between the two matched opponents is
// running right now on FLO or W3C matchmaking. Used as a posting trigger for
// matches whose Liquipedia schedule alone says nothing yet (day-only dates).
func (c *Client) HasLiveGame(ctx context.Context, m liquipedia.Match) bool {
	if len(m.Opponents) < 2 || !m.Opponents[0].Solo() || !m.Opponents[1].Solo() {
		return false
	}
	tag1 := c.tagFor(ctx, m.Opponents[0].Players[0].Name, m.Opponents[0].Display())
	tag2 := c.tagFor(ctx, m.Opponents[1].Players[0].Name, m.Opponents[1].Display())
	if tag1 == "" || tag2 == "" {
		return false
	}
	since := time.Now().Add(-6 * time.Hour)
	if start, err := time.Parse("2006-01-02 15:04:05", m.Date); err == nil {
		since = start.Add(-gameSlack)
	}
	if anyLive(c.floGamesFor(ctx, tag1, tag2, since)) {
		return true
	}
	return c.ongoingLive(ctx, tag1, tag2) != nil
}

// sweep drops long-unused cache entries; the daemon runs for months and the
// per-pair / per-game caches would otherwise grow without bound. At most once
// an hour.
func (c *Client) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.sweepAt) < time.Hour {
		return
	}
	c.sweepAt = time.Now()
	for k, at := range c.h2hAt {
		if time.Since(at) > 48*time.Hour {
			delete(c.h2hAt, k)
			delete(c.h2hGames, k)
		}
	}
	for id, g := range c.games {
		if !g.StartTime.IsZero() && time.Since(g.StartTime) > 7*24*time.Hour {
			delete(c.games, id)
		}
	}
	for tag, e := range c.stats {
		if time.Since(e.at) > 48*time.Hour {
			delete(c.stats, tag)
		}
	}
	for k, m := range c.floSeen {
		if len(m) == 0 {
			delete(c.floSeen, k)
		}
	}
}

// PlayerStatsFor resolves just the ladder stats for each opponent of a match,
// without any game lookups. Meant for the digest, where dozens of matches are
// rendered at once and per-pair game searches would be wasteful.
func (c *Client) PlayerStatsFor(ctx context.Context, m liquipedia.Match) []*PlayerStats {
	out := make([]*PlayerStats, len(m.Opponents))
	for i, o := range m.Opponents {
		if !o.Solo() {
			continue // ladder identity is per-player; skip team opponents
		}
		if tag := c.tagFor(ctx, o.Players[0].Name, o.Display()); tag != "" {
			out[i] = c.statsFor(ctx, tag)
		}
	}
	return out
}

// tagFor resolves a Liquipedia player (page name or display name) to their
// W3Champions battletag via the aka map. "" when unknown.
func (c *Client) tagFor(ctx context.Context, names ...string) string {
	seasons, err := c.currentSeasons(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("w3c: season lookup failed")
		return ""
	}
	if err := c.ensureAka(ctx, seasons); err != nil {
		log.Warn().Err(err).Msg("w3c: aka map refresh failed")
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		if t, ok := c.akaTags[normName(n)]; ok {
			return t
		}
	}
	return ""
}

// statsFor returns cached-or-fresh ladder stats for a battletag, trying the
// current season first and falling back to the previous one (a fresh season
// has few games in it). A failed refresh keeps the last good value so cards
// don't flicker.
func (c *Client) statsFor(ctx context.Context, tag string) *PlayerStats {
	c.mu.Lock()
	entry, ok := c.stats[tag]
	c.mu.Unlock()
	if ok && time.Since(entry.at) < statsTTL {
		return entry.s
	}
	seasons, err := c.currentSeasons(ctx)
	if err != nil {
		if ok {
			return entry.s
		}
		return nil
	}
	// A hanging API must not eat minutes per player: with route rotation the
	// worst case is otherwise several timeouts per season per lookup.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var s *PlayerStats
	for _, season := range seasons {
		if s, err = c.fetchStats(ctx, tag, season); s != nil {
			break
		}
	}
	if s == nil {
		if err != nil {
			log.Warn().Err(err).Str("tag", tag).Msg("w3c: stats fetch failed")
		}
		if ok {
			return entry.s // sticky: keep showing the stale value
		}
		return nil
	}
	c.mu.Lock()
	c.stats[tag] = statsEntry{s: s, at: time.Now()}
	c.mu.Unlock()
	return s
}

// fetchStats picks the player's main 1v1 row (most games this season).
func (c *Client) fetchStats(ctx context.Context, tag string, season int) (*PlayerStats, error) {
	var rows []struct {
		GameMode    int `json:"gameMode"`
		MMR         int `json:"mmr"`
		Rank        int `json:"rank"`
		LeagueOrder int `json:"leagueOrder"`
		Games       int `json:"games"`
	}
	path := fmt.Sprintf("/api/players/%s/game-mode-stats?gateWay=%d&season=%d", url.PathEscape(tag), gateway, season)
	if err := c.getJSON(ctx, path, &rows); err != nil {
		return nil, err
	}
	best := -1
	for i, r := range rows {
		if r.GameMode != gameMode1v1 || r.Games == 0 {
			continue
		}
		if best == -1 || r.Games > rows[best].Games {
			best = i
		}
	}
	if best == -1 {
		return nil, fmt.Errorf("w3c: %s has no 1v1 games in season %d", tag, season)
	}
	return &PlayerStats{
		BattleTag:   tag,
		Season:      season,
		MMR:         rows[best].MMR,
		Rank:        rows[best].Rank,
		LeagueOrder: rows[best].LeagueOrder,
	}, nil
}

// h2hSince returns finished W3C games between two players since a start time,
// newest data cached: the search itself is throttled to once per 2 minutes per
// pair, and finished game details are fetched once and cached forever.
func (c *Client) h2hSince(ctx context.Context, tag1, tag2 string, since time.Time) []*GameStats {
	key := tag1 + "|" + tag2

	c.mu.Lock()
	last := c.h2hAt[key]
	cached := c.h2hGames[key]
	c.mu.Unlock()
	if time.Since(last) < 2*time.Minute {
		return cached
	}

	// Time-boxed: with a hanging API the search must not starve the tick.
	hctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ids := c.h2hMatchIDs(hctx, tag1, tag2, since)
	var games []*GameStats
	for _, id := range ids {
		if g := c.gameDetail(hctx, id, tag1, tag2); g != nil {
			games = append(games, g)
		}
	}
	// Keep the previous result on a fully-failed refresh (sticky).
	if games == nil && cached != nil {
		games = cached
	}
	c.mu.Lock()
	c.h2hAt[key] = time.Now()
	c.h2hGames[key] = games
	c.mu.Unlock()
	return games
}

// h2hMatchIDs lists finished 1v1 games between the two tags after `since`,
// oldest first. The search is per-season, and a match near a season boundary
// can land in the previous one, so both recent seasons are tried.
func (c *Client) h2hMatchIDs(ctx context.Context, tag1, tag2 string, since time.Time) []string {
	seasons, err := c.currentSeasons(ctx)
	if err != nil {
		return nil
	}
	type row struct {
		ID        string `json:"id"`
		StartTime string `json:"startTime"`
	}
	fetch := func(a, b string, season int) []row {
		var resp struct {
			Matches []row `json:"matches"`
		}
		path := fmt.Sprintf("/api/matches/search?playerId=%s&opponentId=%s&gateway=%d&offset=0&pageSize=20&season=%d",
			url.QueryEscape(a), url.QueryEscape(b), gateway, season)
		if err := c.getJSON(ctx, path, &resp); err != nil {
			return nil
		}
		return resp.Matches
	}
	var rows []row
	for _, season := range seasons {
		if rows = fetch(tag1, tag2, season); len(rows) > 0 {
			break
		}
	}
	var ids []string
	for i := len(rows) - 1; i >= 0; i-- { // API returns newest first
		t, err := time.Parse(time.RFC3339, rows[i].StartTime)
		if err != nil || t.Before(since) {
			continue
		}
		ids = append(ids, rows[i].ID)
	}
	return ids
}

// gameDetail fetches (or returns cached) per-game stats for a finished game.
func (c *Client) gameDetail(ctx context.Context, id, tag1, tag2 string) *GameStats {
	c.mu.Lock()
	if g, ok := c.games[id]; ok {
		c.mu.Unlock()
		return g
	}
	c.mu.Unlock()

	var resp struct {
		Match struct {
			MapName           string `json:"mapName"`
			DurationInSeconds int    `json:"durationInSeconds"`
			StartTime         string `json:"startTime"`
			Teams             []struct {
				Players []struct {
					BattleTag string `json:"battleTag"`
					Won       bool   `json:"won"`
					Race      int    `json:"race"`
					RndRace   *int   `json:"rndRace"`
				} `json:"players"`
			} `json:"teams"`
		} `json:"match"`
		PlayerScores []struct {
			BattleTag string `json:"battleTag"`
			Heroes    []struct {
				Name  string `json:"name"`
				Level int    `json:"level"`
			} `json:"heroes"`
			UnitScore struct {
				UnitsProduced int `json:"unitsProduced"`
				UnitsKilled   int `json:"unitsKilled"`
			} `json:"unitScore"`
		} `json:"playerScores"`
	}
	if err := c.getJSON(ctx, "/api/matches/"+url.PathEscape(id), &resp); err != nil {
		return nil
	}

	start, _ := time.Parse(time.RFC3339, resp.Match.StartTime)
	g := &GameStats{
		ID:        id,
		Map:       resp.Match.MapName,
		StartTime: start,
		Duration:  time.Duration(resp.Match.DurationInSeconds) * time.Second,
	}
	sideOf := func(tag string) int {
		if tag == tag1 {
			return 0
		}
		if tag == tag2 {
			return 1
		}
		return -1
	}
	for _, t := range resp.Match.Teams {
		for _, p := range t.Players {
			if i := sideOf(p.BattleTag); i >= 0 {
				g.Players[i].BattleTag = p.BattleTag
				g.Players[i].Won = p.Won
				race := p.Race
				if p.RndRace != nil { // race the Random roll resolved into
					race = *p.RndRace
				}
				g.Players[i].Race = raceName(race)
			}
		}
	}
	for _, ps := range resp.PlayerScores {
		i := sideOf(ps.BattleTag)
		if i < 0 {
			continue
		}
		g.Players[i].UnitsProduced = ps.UnitScore.UnitsProduced
		g.Players[i].UnitsKilled = ps.UnitScore.UnitsKilled
		for _, h := range ps.Heroes {
			g.Players[i].Heroes = append(g.Players[i].Heroes, HeroStat{Name: heroName(h.Name), Level: h.Level})
		}
	}
	if g.Players[0].BattleTag == "" || g.Players[1].BattleTag == "" {
		return nil // not actually a game between these two tags
	}
	c.mu.Lock()
	c.games[id] = g
	c.mu.Unlock()
	return g
}

// currentSeasons returns the latest two ladder season ids, newest first
// (cached for a day so a season rollover is picked up).
func (c *Client) currentSeasons(ctx context.Context) ([]int, error) {
	c.mu.Lock()
	cached := c.seasons
	fresh := time.Since(c.seasonsAt) < 24*time.Hour
	c.mu.Unlock()
	if len(cached) > 0 && fresh {
		return cached, nil
	}
	var seasons []struct {
		ID int `json:"id"`
	}
	if err := c.getJSON(ctx, "/api/ladder/seasons", &seasons); err != nil {
		if len(cached) > 0 {
			return cached, nil // sticky: an old list beats none
		}
		return nil, err
	}
	if len(seasons) == 0 {
		return nil, fmt.Errorf("w3c: empty seasons list")
	}
	out := []int{seasons[0].ID}
	if len(seasons) > 1 {
		out = append(out, seasons[1].ID)
	}
	c.mu.Lock()
	c.seasons = out
	c.seasonsAt = time.Now()
	c.mu.Unlock()
	return out, nil
}

// ensureAka guarantees a usable aka map. The first fill is synchronous; once a
// map exists, expiry triggers a background refresh while lookups keep serving
// the stale map (stale-while-revalidate). Attempts are throttled so a flaky
// API is not hammered on every render.
func (c *Client) ensureAka(ctx context.Context, seasons []int) error {
	c.mu.Lock()
	have := c.akaTags != nil
	ttl := akaTTL
	if c.akaPartial { // incomplete scan (throttled): try to complete it sooner
		ttl = 30 * time.Minute
	}
	fresh := have && time.Since(c.akaFetched) < ttl
	throttled := time.Since(c.akaAttempt) < 10*time.Minute
	if fresh || c.akaBusy || throttled {
		c.mu.Unlock()
		if fresh || have {
			return nil
		}
		return fmt.Errorf("w3c: aka map unavailable (recent refresh failed)")
	}
	c.akaAttempt = time.Now()
	if have { // refresh in the background, keep serving the stale map
		c.akaBusy = true
		c.mu.Unlock()
		go func() {
			bctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := c.refreshAka(bctx, seasons); err != nil {
				log.Warn().Err(err).Msg("w3c: background aka refresh failed")
			}
			c.mu.Lock()
			c.akaBusy = false
			c.mu.Unlock()
		}()
		return nil
	}
	c.mu.Unlock()
	// First fill is synchronous but time-boxed: when the API throttles us it
	// hangs rather than erroring, and an unbounded scan would starve the rest
	// of the enrichment (stats, FLO) of context budget.
	fillCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return c.refreshAka(fillCtx, seasons)
}

// refreshAka scans the top league rankings, which carry playerAkaData for
// known pros. Both recent seasons are scanned: early in a season most pros
// have not laddered yet, so the previous season fills the gaps
// (current-season tags win on conflict).
func (c *Client) refreshAka(ctx context.Context, seasons []int) error {
	tags := map[string]string{}
	failed := 0
	first := true
	// League-major order: the top leagues of both seasons go first, so a scan
	// cut short by throttling still catches the star players.
	for _, league := range akaLeagues {
		for _, season := range seasons {
			// Pace the scan: burst requests are what get this IP throttled.
			if !first {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1500 * time.Millisecond):
				}
			}
			first = false
			var rows []struct {
				Player struct {
					PlayerIDs []struct {
						BattleTag string `json:"battleTag"`
					} `json:"playerIds"`
				} `json:"player"`
				PlayersInfo []struct {
					PlayerAkaData *struct {
						Name       string `json:"name"`
						Liquipedia string `json:"liquipedia"`
					} `json:"playerAkaData"`
				} `json:"playersInfo"`
			}
			path := fmt.Sprintf("/api/ladder/%d?gateWay=%d&gameMode=%d&season=%d", league, gateway, gameMode1v1, season)
			leagueCtx, cancelLeague := context.WithTimeout(ctx, 45*time.Second)
			err := c.getJSON(leagueCtx, path, &rows)
			cancelLeague()
			if err != nil {
				// One slow/failing league must not sink the whole scan.
				log.Warn().Err(err).Int("league", league).Int("season", season).Msg("w3c: league scan failed; skipping")
				failed++
				continue
			}
			for _, r := range rows {
				if len(r.Player.PlayerIDs) == 0 || len(r.PlayersInfo) == 0 {
					continue
				}
				aka := r.PlayersInfo[0].PlayerAkaData
				if aka == nil {
					continue
				}
				tag := r.Player.PlayerIDs[0].BattleTag
				for _, key := range []string{aka.Liquipedia, aka.Name} {
					if k := normName(key); k != "" {
						if _, seen := tags[k]; !seen { // higher league, then newer season wins
							tags[k] = tag
						}
					}
				}
			}
		}
	}
	if len(tags) == 0 && failed > 0 {
		return fmt.Errorf("w3c: aka scan failed for all %d leagues", failed)
	}
	c.mu.Lock()
	if failed > 0 { // partial scan must not lose names an earlier scan found
		for k, v := range c.akaTags {
			if _, ok := tags[k]; !ok {
				tags[k] = v
			}
		}
	}
	c.akaTags = tags
	c.akaFetched = time.Now()
	c.akaPartial = failed > 0
	c.mu.Unlock()
	c.saveCache()
	log.Info().Int("names", len(tags)).Int("skippedLeagues", failed).Msg("w3c: aka map refreshed")
	return nil
}

// getJSON fetches an API path, rotating through the egress routes: the route
// that worked last is tried first; on failure the next one gets a shot. A
// route that succeeds after a failover becomes preferred, so a throttled
// direct connection stops costing a timeout on every call.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, c.base+path, nil, out)
}

func (c *Client) doJSON(ctx context.Context, method, fullURL string, body []byte, out any) error {
	c.mu.Lock()
	start := c.hcPref
	n := len(c.hcs)
	c.mu.Unlock()
	attempts := n
	if attempts < 2 {
		attempts = 2 // single route still deserves one retry
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		idx := (start + i) % n
		if i > 0 {
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(500 * time.Millisecond):
			}
		}
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, rdr)
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", "wc3-tracker/0.1 (https://github.com/virusalex/wc3matches)")
		resp, err := c.hcs[idx].Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("w3c: %s: HTTP %d", fullURL, resp.StatusCode)
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if idx != start {
			c.mu.Lock()
			c.hcPref = idx
			c.mu.Unlock()
			log.Info().Str("egress", c.hcLabels[idx]).Msg("w3c: switched egress route")
		}
		return nil
	}
	// Nothing worked: rotate the starting point so the next call does not
	// begin with the same dead route.
	c.mu.Lock()
	c.hcPref = (start + 1) % n
	c.mu.Unlock()
	return lastErr
}

// raceName maps the W3C race enum to a readable name.
func raceName(r int) string {
	switch r {
	case 0:
		return "Random"
	case 1:
		return "Human"
	case 2:
		return "Orc"
	case 4:
		return "Night Elf"
	case 8:
		return "Undead"
	}
	return ""
}

// heroName maps a W3C hero icon id to a readable name.
func heroName(icon string) string {
	if n, ok := heroNames[icon]; ok {
		return n
	}
	return icon
}

var heroNames = map[string]string{
	"archmage":           "Archmage",
	"mountainking":       "Mountain King",
	"paladin":            "Paladin",
	"sorceror":           "Blood Mage",
	"blademaster":        "Blademaster",
	"farseer":            "Far Seer",
	"taurenchieftain":    "Tauren Chieftain",
	"shadowhunter":       "Shadow Hunter",
	"deathknight":        "Death Knight",
	"lich":               "Lich",
	"dreadlord":          "Dread Lord",
	"cryptlord":          "Crypt Lord",
	"demonhunter":        "Demon Hunter",
	"keeperofthegrove":   "Keeper of the Grove",
	"priestessofthemoon": "Priestess of the Moon",
	"warden":             "Warden",
	"alchemist":          "Goblin Alchemist",
	"avatarofflame":      "Firelord",
	"bansheeranger":      "Dark Ranger",
	"beastmaster":        "Beastmaster",
	"pandarenbrewmaster": "Pandaren Brewmaster",
	"pitlord":            "Pit Lord",
	"seawitch":           "Naga Sea Witch",
	"tinker":             "Goblin Tinker",
}

// normName normalizes a player name for aka-map lookups: Liquipedia page names
// use underscores ("Cloud_(American_player)") while display names use spaces.
func normName(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", " "))
}
