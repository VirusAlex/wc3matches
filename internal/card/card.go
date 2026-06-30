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
)

// Render builds the rich-HTML body for a match. now is used to decide whether an
// unfinished match has already started (LIVE) vs is still upcoming.
func Render(m liquipedia.Match, now time.Time) string {
	var b strings.Builder

	o1, o2 := opponent(m, 0), opponent(m, 1)
	writeTitle(&b, m, o1, o2)
	writeStatus(&b, m, now, o1, o2)
	writeGames(&b, m, o1, o2)

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

func writeStatus(b *strings.Builder, m liquipedia.Match, now time.Time, o1, o2 liquipedia.Opponent) {
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
	}
	b.WriteString("</p>")
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

// writeGames renders each played map as a block inside a collapsible section:
// map name + winner, then each player's hero picks (Liquipedia has no levels).
func writeGames(b *strings.Builder, m liquipedia.Match, o1, o2 liquipedia.Opponent) {
	played := 0
	for _, g := range m.Games {
		if gamePlayed(g) {
			played++
		}
	}
	if played == 0 {
		return
	}
	_ = played
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
		winSide := gameWinnerSide(g)
		// One collapsible per map: summary shows number, map and winner; the body
		// holds the map image and the player/race/heroes table.
		summary := fmt.Sprintf("🗺️ %d. <b>%s</b>", gi, esc(mapName))
		if winName := sideName(winSide, o1, o2); winName != "" {
			summary += " · 🏆 " + esc(winName)
		}
		fmt.Fprintf(b, "<details><summary>%s</summary>", summary)
		if img := mapImageURL(g.Map); img != "" {
			fmt.Fprintf(b, "<img src=\"%s\">", img)
		}
		b.WriteString(`<table border="1"><tr><th align="left">Player</th><th align="left">Race</th><th align="left">Heroes</th></tr>`)
		writeGameRow(b, g, 0, o1, winSide == 0)
		writeGameRow(b, g, 1, o2, winSide == 1)
		b.WriteString("</table></details>")
	}
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
// heroes. Per-game faction is preferred (a player may go Random).
func writeGameRow(b *strings.Builder, g liquipedia.Game, side int, o liquipedia.Opponent, won bool) {
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
	race := gop.Faction()
	if race == "" {
		race = o.Faction()
	}
	heroes := "?"
	if hs := gop.Heroes(); len(hs) > 0 {
		esced := make([]string, len(hs))
		for i, h := range hs {
			esced[i] = esc(h)
		}
		heroes = strings.Join(esced, ", ")
	}
	fmt.Fprintf(b, "<tr><td align=\"left\">%s</td><td align=\"left\">%s</td><td align=\"left\">%s</td></tr>",
		player, esc(FactionName(race)), heroes)
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
