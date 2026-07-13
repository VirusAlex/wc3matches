// Package card renders a Telegram rich-message card for a WarCraft III match
// from LiquipediaDB match2 data. Output is the same rich-HTML dialect used by
// the HLTV tracker (Bot API 10.1): headings, tables, expandable sections.
// Liquipedia data is CC-BY-SA 3.0, so every card links back to the source.
package card

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

// Extras is optional enrichment rendered into a card when available.
type Extras struct {
	W3C      *w3c.Enrichment
	PrizeUSD float64 // tournament prize pool in USD; 0 = unknown
}

// Render builds the rich-HTML body for a match. now is used to decide whether an
// unfinished match has already started (LIVE) vs is still upcoming.
func Render(m liquipedia.Match, now time.Time) string {
	return RenderExtras(m, now, nil)
}

// RenderEnriched renders the card with optional W3Champions data (kept as a
// convenience wrapper around RenderExtras).
func RenderEnriched(m liquipedia.Match, now time.Time, e *w3c.Enrichment) string {
	return RenderExtras(m, now, &Extras{W3C: e})
}

// RenderExtras renders the card with all optional enrichment: W3Champions
// ladder context / per-game stats and the tournament prize pool.
func RenderExtras(m liquipedia.Match, now time.Time, x *Extras) string {
	var b strings.Builder

	o1, o2 := opponent(m, 0), opponent(m, 1)
	var e *w3c.Enrichment
	prize := 0.0
	if x != nil {
		e, prize = x.W3C, x.PrizeUSD
	}
	writeTitle(&b, m, o1, o2)
	writeStatus(&b, m, now, o1, o2, prize)
	var stats []*w3c.PlayerStats
	var games []*w3c.GameStats
	curSeason := 0
	if e != nil {
		stats, games, curSeason = e.Stats, e.Games, e.Season
	}
	writeLadder(&b, stats, curSeason, o1, o2)
	writeGames(&b, m, now, o1, o2, games)

	// Action links: live stream / VOD / head-to-head.
	var links []string
	if m.Finished != 1 {
		if s := m.StreamURL(); s != "" {
			links = append(links, fmt.Sprintf("<a href=\"%s\">▶️ Watch</a>", esc(s)))
		}
	} else if m.Vod != "" {
		links = append(links, fmt.Sprintf("<a href=\"%s\">🎞️ VOD</a>", esc(m.Vod)))
	}
	if m.Links.HeadToHead != "" {
		links = append(links, fmt.Sprintf("<a href=\"%s\">⚔️ H2H</a>", esc(m.Links.HeadToHead)))
	}
	if len(links) > 0 {
		fmt.Fprintf(&b, "<p>%s</p>", strings.Join(links, " · "))
	}

	// Attribution (required by CC-BY-SA) + source backlink to the match page.
	// No render timestamp here: it would change every tick and force a pointless
	// edit even when the match data is unchanged.
	fmt.Fprintf(&b, "<p>📊 Source: <a href=\"%s\">Liquipedia</a> (CC-BY-SA)</p>", liquipediaURL(m))
	return b.String()
}

func writeTitle(b *strings.Builder, m liquipedia.Match, o1, o2 liquipedia.Opponent) {
	left := withFlagLeft(o1)
	right := withFlagRight(o2)
	// Show a score only when it is actually known; a finished-but-not-played
	// match has -1 scores and must not render a fake "0 : 0".
	if scoreKnown(o1, o2) {
		mid := fmt.Sprintf("%d : %d", o1.Score, o2.Score)
		switch m.Winner {
		case "1":
			mid = "🏆 " + mid
		case "2":
			mid = mid + " 🏆"
		}
		fmt.Fprintf(b, "<h3>%s  %s  %s</h3>", left, mid, right)
		return
	}
	fmt.Fprintf(b, "<h3>%s  vs  %s</h3>", left, right)
}

// scoreKnown reports whether both opponents have a real (non-negative) score.
func scoreKnown(o1, o2 liquipedia.Opponent) bool {
	return o1.Score >= 0 && o2.Score >= 0
}

func notPlayed(m liquipedia.Match) bool { return m.ResultType == "np" }
func walkover(m liquipedia.Match) bool  { return m.Walkover != "" || m.ResultType == "default" }

func writeStatus(b *strings.Builder, m liquipedia.Match, now time.Time, o1, o2 liquipedia.Opponent, prizeUSD float64) {
	var head string
	switch {
	case m.Finished == 1 && notPlayed(m):
		head = "🚫 <b>Not played</b>"
	case m.Finished == 1 && walkover(m):
		head = "✅ <b>Finished</b> (walkover)"
	case m.Finished == 1:
		head = "✅ <b>Finished</b>"
	case started(m, now):
		head = "🔴 <b>LIVE</b>"
	default:
		head = "⏰ <b>Upcoming</b>"
	}
	b.WriteString("<p>" + head)
	if m.BestOf > 0 {
		fmt.Fprintf(b, " · Bo%d", m.BestOf)
	}
	if st := m.Stage(); st != "" {
		b.WriteString(" · <b>" + esc(st) + "</b>")
	}
	if t := tierLabel(m.LiquipediaTier); t != "" {
		label := t
		if m.LiquipediaTierType != "" && !strings.EqualFold(m.LiquipediaTierType, "main event") {
			label += " · " + esc(m.LiquipediaTierType)
		}
		b.WriteString(" · " + label)
	}
	if g := gameLabel(m.Game); g != "" {
		b.WriteString(" · " + g)
	}
	if st := matchTimeUTC(m); st != "?" {
		fmt.Fprintf(b, "<br>🕐 %s", st)
	}
	if m.Tournament != "" {
		fmt.Fprintf(b, "<br>📍 %s", esc(m.Tournament))
		if prizeUSD > 0 {
			fmt.Fprintf(b, " · 💰 %s", FormatUSD(prizeUSD))
		}
	}
	b.WriteString("</p>")
}

// FormatUSD renders a prize pool as whole dollars with thousands separators,
// e.g. "$3,664".
func FormatUSD(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	return "$" + string(out)
}

// gameLabel maps the game key to a readable name.
func gameLabel(g string) string {
	switch g {
	case "reforged":
		return "Reforged"
	case "frozenthrone":
		return "TFT"
	}
	return g
}

// writeLadder renders the W3Champions ladder context table (1v1 MMR and league
// rank). Rows from an older season are marked, since early in a fresh season
// many pros have no games yet. Skipped entirely when neither player has stats.
func writeLadder(b *strings.Builder, stats []*w3c.PlayerStats, curSeason int, o1, o2 liquipedia.Opponent) {
	var any bool
	for _, s := range stats {
		if s != nil {
			any = true
		}
	}
	if !any {
		return
	}
	b.WriteString("<p>📈 <b>W3Champions ladder</b></p>")
	b.WriteString(`<table border="1"><tr><th align="left">Player</th><th>MMR</th><th>Rank</th></tr>`)
	opps := []liquipedia.Opponent{o1, o2}
	for i, o := range opps {
		if i >= len(stats) || stats[i] == nil {
			continue
		}
		s := stats[i]
		player := esc(o.Display())
		if e := FlagEmoji(o.Flag()); e != "" {
			player = e + " " + player
		}
		rank := "-"
		if s.Rank > 0 {
			rank = fmt.Sprintf("#%d %s", s.Rank, s.League())
		}
		if curSeason > 0 && s.Season != curSeason {
			rank += fmt.Sprintf(" (s%d)", s.Season)
		}
		fmt.Fprintf(b, `<tr><td align="left">%s</td><td>%d</td><td>%s</td></tr>`,
			player, s.MMR, esc(rank))
	}
	b.WriteString("</table>")
}

// writeGames renders each played map as a block inside a collapsible section:
// map name + winner, then each player's hero picks. When a matching W3Champions
// or FLO game is known, the section also carries the map duration, final hero
// levels and units produced/killed. Games with no Liquipedia counterpart yet
// (Liquipedia lags behind the live game) are rendered as their own sections,
// including tournament games currently running on FLO.
func writeGames(b *strings.Builder, m liquipedia.Match, now time.Time, o1, o2 liquipedia.Opponent, w3cGames []*w3c.GameStats) {
	usedW3C := make([]bool, len(w3cGames))
	takeW3C := func(mapName string) *w3c.GameStats {
		key := w3c.NormMapKey(mapName)
		if key == "" {
			return nil
		}
		for i, g := range w3cGames {
			if !usedW3C[i] && w3c.NormMapKey(g.Map) == key {
				usedW3C[i] = true
				return g
			}
		}
		return nil
	}

	writeOne := func(gi int, g liquipedia.Game, mapName string, winSide int, wg *w3c.GameStats) {
		summary := fmt.Sprintf("🗺️ %d. <b>%s</b>", gi, esc(mapName))
		if winName := sideName(winSide, o1, o2); winName != "" {
			summary += " · 🏆 " + esc(winName)
		}
		if wg != nil {
			switch {
			case wg.Live:
				summary += " · 🔴 in progress"
				if mins := int(now.Sub(wg.StartTime).Minutes()); mins >= 0 {
					summary += fmt.Sprintf(" · ⏱ %d min", mins)
				}
			case wg.Duration > 0:
				summary += " · ⏱ " + durShort(wg.Duration)
			}
		}
		fmt.Fprintf(b, "<details><summary>%s</summary>", summary)
		if img := mapImageURL(mapName); img != "" {
			fmt.Fprintf(b, "<img src=\"%s\">", img)
		}
		writeGameTable(b, g, o1, o2, winSide, wg)
		b.WriteString("</details>")
	}

	gi := 0
	for _, g := range m.Games {
		if !gamePlayed(g) {
			continue
		}
		gi++
		mapName := g.Map
		if mapName == "" {
			mapName = "?"
		}
		writeOne(gi, g, mapName, gameWinnerSide(g), takeW3C(g.Map))
	}

	// Games not on Liquipedia yet: matchmaking history or the FLO live window.
	for i, wg := range w3cGames {
		if usedW3C[i] {
			continue
		}
		gi++
		winSide := -1
		if wg.Players[0].Won {
			winSide = 0
		} else if wg.Players[1].Won {
			winSide = 1
		}
		writeOne(gi, liquipedia.Game{}, wg.Map, winSide, wg)
	}
}

// writeGameTable renders the per-map stats table. Columns adapt to the data:
// heroes when any source has picks, units when replay stats are present
// (FLO-only games carry just player + race).
func writeGameTable(b *strings.Builder, g liquipedia.Game, o1, o2 liquipedia.Opponent, winSide int, wg *w3c.GameStats) {
	hasHeroes := false
	hasUnits := false
	if wg != nil {
		for _, p := range wg.Players {
			if len(p.Heroes) > 0 {
				hasHeroes = true
			}
			if p.UnitsProduced > 0 || p.UnitsKilled > 0 {
				hasUnits = true
			}
		}
	}
	for _, gop := range g.Opponents {
		if len(gop.Heroes()) > 0 {
			hasHeroes = true
		}
	}
	if wg == nil {
		hasHeroes = true // pure-Liquipedia layout always shows the column
	}
	b.WriteString(`<table border="1"><tr><th align="left">Player</th><th align="left">Race</th>`)
	if hasHeroes {
		b.WriteString(`<th align="left">Heroes</th>`)
	}
	if hasUnits {
		b.WriteString(`<th>Units made/killed</th>`)
	}
	b.WriteString(`</tr>`)
	writeGameRow(b, g, 0, o1, winSide == 0, wg, hasHeroes, hasUnits)
	writeGameRow(b, g, 1, o2, winSide == 1, wg, hasHeroes, hasUnits)
	b.WriteString("</table>")
}

// sideName returns the display name of opponent side 0/1, or "" for -1.
func sideName(side int, o1, o2 liquipedia.Opponent) string {
	switch side {
	case 0:
		return o1.Display()
	case 1:
		return o2.Display()
	}
	return ""
}

// writeGameRow renders one player's row for a game: player (🏆 flag nick), race,
// heroes (with final levels when W3C data is present), and units. Per-game
// faction is preferred (a player may go Random).
func writeGameRow(b *strings.Builder, g liquipedia.Game, side int, o liquipedia.Opponent, won bool, wg *w3c.GameStats, hasHeroes, hasUnits bool) {
	var gop liquipedia.GameOpponent
	if side < len(g.Opponents) {
		gop = g.Opponents[side]
	}
	player := esc(o.Display())
	if e := FlagEmoji(o.Flag()); e != "" {
		player = e + " " + player
	}
	if won {
		player = "🏆 " + player
	}
	raceLabel := FactionName(gop.Faction())
	if raceLabel == "" {
		raceLabel = FactionName(o.Faction())
	}

	var wp *w3c.GamePlayerStats
	if wg != nil {
		wp = &wg.Players[side]
	}
	if wp != nil && raceLabel == "" {
		raceLabel = wp.Race
	}

	fmt.Fprintf(b, "<tr><td align=\"left\">%s</td><td align=\"left\">%s</td>", player, esc(raceLabel))
	if hasHeroes {
		// Heroes: prefer W3C (has final levels), fall back to Liquipedia picks.
		heroes := "?"
		if wp != nil && len(wp.Heroes) > 0 {
			parts := make([]string, len(wp.Heroes))
			for i, h := range wp.Heroes {
				parts[i] = fmt.Sprintf("%s (%d)", esc(h.Name), h.Level)
			}
			heroes = strings.Join(parts, ", ")
		} else if hs := gop.Heroes(); len(hs) > 0 {
			esced := make([]string, len(hs))
			for i, h := range hs {
				esced[i] = esc(h)
			}
			heroes = strings.Join(esced, ", ")
		}
		fmt.Fprintf(b, "<td align=\"left\">%s</td>", heroes)
	}
	if hasUnits {
		if wp != nil && (wp.UnitsProduced > 0 || wp.UnitsKilled > 0) {
			fmt.Fprintf(b, "<td>%d / %d</td>", wp.UnitsProduced, wp.UnitsKilled)
		} else {
			b.WriteString("<td>-</td>")
		}
	}
	b.WriteString("</tr>")
}

func durShort(d time.Duration) string {
	total := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func gamePlayed(g liquipedia.Game) bool {
	return len(g.Scores) >= 2 && (g.Scores[0] != 0 || g.Scores[1] != 0)
}

// gameWinnerSide returns 0 or 1 for the winning opponent, or -1 if undecided.
func gameWinnerSide(g liquipedia.Game) int {
	if g.Winner == "1" {
		return 0
	}
	if g.Winner == "2" {
		return 1
	}
	if len(g.Scores) >= 2 {
		if g.Scores[0] > g.Scores[1] {
			return 0
		}
		if g.Scores[1] > g.Scores[0] {
			return 1
		}
	}
	return -1
}

func opponent(m liquipedia.Match, i int) liquipedia.Opponent {
	if i < len(m.Opponents) {
		return m.Opponents[i]
	}
	return liquipedia.Opponent{}
}

// withFlagLeft / withFlagRight bookend the player name with their flag and add
// the full race name in italics.
func withFlagLeft(o liquipedia.Opponent) string {
	name := esc(o.Display())
	if r := FactionName(o.Faction()); r != "" {
		name += " <i>· " + r + "</i>"
	}
	if e := FlagEmoji(o.Flag()); e != "" {
		return e + " " + name
	}
	return name
}

func withFlagRight(o liquipedia.Opponent) string {
	name := esc(o.Display())
	if r := FactionName(o.Faction()); r != "" {
		name = "<i>" + r + " ·</i> " + name
	}
	if e := FlagEmoji(o.Flag()); e != "" {
		return name + " " + e
	}
	return name
}

func started(m liquipedia.Match, now time.Time) bool {
	// Day-only dates pass at midnight; require real liveness evidence instead.
	if m.DateExact == 0 {
		return m.LiveEvidence()
	}
	t, err := parseMatchTime(m)
	if err != nil {
		return false
	}
	return !t.After(now)
}

func parseMatchTime(m liquipedia.Match) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05", m.Date) // Liquipedia dates are UTC
}

func matchTimeUTC(m liquipedia.Match) string {
	t, err := parseMatchTime(m)
	if err != nil {
		return "?"
	}
	if m.DateExact == 0 {
		return t.UTC().Format("Jan 2") + " (time TBD)"
	}
	return t.UTC().Format("Jan 2, 15:04") + " UTC"
}

func tierLabel(tier string) string {
	switch tier {
	case "1":
		return "S-Tier"
	case "2":
		return "A-Tier"
	case "3":
		return "B-Tier"
	case "4":
		return "C-Tier"
	case "5":
		return "Qualifier"
	}
	return ""
}

func liquipediaURL(m liquipedia.Match) string {
	page := m.PageName
	if page == "" {
		page = m.Parent
	}
	if page == "" {
		return "https://liquipedia.net/warcraft/"
	}
	return "https://liquipedia.net/warcraft/" + page
}

func esc(s string) string { return html.EscapeString(s) }
