package daemon

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/virusalex/wc3-tracker/internal/card"
	"github.com/virusalex/wc3-tracker/internal/liquipedia"
)

// SendDailyDigest posts a schedule of interesting matches for the next 24h,
// grouped by tournament. Exact-timed matches show their time; matches with a
// floating bracket time are listed without a time (they'll appear live).
func (d *Daemon) SendDailyDigest(ctx context.Context, ref time.Time) error {
	favorites, err := d.store.Favorites(ctx)
	if err != nil {
		return err
	}
	start := lastUTCAnchor(ref, 7)
	end := start.Add(24 * time.Hour)
	cond := fmt.Sprintf("[[finished::0]] AND [[date::>%s]] AND [[date::<%s]]",
		start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05"))
	matches, err := d.api.Matches(ctx, cond, "date ASC", 100)
	if err != nil {
		return err
	}

	var interestingMatches []liquipedia.Match
	for _, m := range matches {
		if interesting(m, favorites, d.cfg.TierMax) {
			interestingMatches = append(interestingMatches, m)
		}
	}

	title := "📅 WarCraft III matches for " + dateHuman(start)
	if len(interestingMatches) == 0 {
		_, err := d.bot.SendRich(ctx, fmt.Sprintf("<h3>%s</h3><p>No notable matches 🤷</p>"+attribution(), title))
		return err
	}

	// Group by tournament, preserving first-seen (chronological) order.
	var order []string
	byEvent := map[string][]liquipedia.Match{}
	for _, m := range interestingMatches {
		if _, ok := byEvent[m.Tournament]; !ok {
			order = append(order, m.Tournament)
		}
		byEvent[m.Tournament] = append(byEvent[m.Tournament], m)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<h3>%s</h3>", title)
	for _, ev := range order {
		ms := byEvent[ev]
		label := ev
		if t := tierShort(ms[0].LiquipediaTier); t != "" {
			label += " · " + t
		}
		fmt.Fprintf(&b, "<p>🏆 <b>%s</b></p>", html.EscapeString(label))

		var exact, floating []liquipedia.Match
		for _, m := range ms {
			if m.DateExact == 1 {
				exact = append(exact, m)
			} else {
				floating = append(floating, m)
			}
		}
		if len(exact) > 0 {
			b.WriteString(`<table border="1"><tr><th>Time (UTC)</th><th align="left">Match</th><th>Bo</th></tr>`)
			for _, m := range exact {
				t, _ := time.Parse("2006-01-02 15:04:05", m.Date)
				fmt.Fprintf(&b, "<tr><td>%s</td><td align=\"left\">%s</td><td>%d</td></tr>",
					t.UTC().Format("15:04"), pairing(m), m.BestOf)
			}
			b.WriteString("</table>")
		}
		if len(floating) > 0 {
			b.WriteString("<p><i>🕐 Time TBD (will appear live):</i></p>")
			seen := map[string]bool{}
			for _, m := range floating {
				p := pairing(m)
				if seen[p] {
					continue
				}
				seen[p] = true
				fmt.Fprintf(&b, "<p>• %s</p>", p)
			}
		}
	}
	b.WriteString(attribution())

	if _, err := d.bot.SendRich(ctx, b.String()); err != nil {
		return err
	}
	log.Info().Int("matches", len(interestingMatches)).Msg("daily digest sent")
	return nil
}

// pairing renders "🇰🇷 Moon (Night Elf) vs 🇨🇳 Infi (Human)" for a digest row.
func pairing(m liquipedia.Match) string {
	side := func(i int) string {
		if i >= len(m.Opponents) {
			return "TBD"
		}
		o := m.Opponents[i]
		s := html.EscapeString(o.Display())
		if e := card.FlagEmoji(o.Flag()); e != "" {
			s = e + " " + s
		}
		if r := card.FactionName(o.Faction()); r != "" {
			s += " <i>(" + r + ")</i>"
		}
		return s
	}
	return side(0) + " vs " + side(1)
}

func tierShort(tier string) string {
	switch tier {
	case "1":
		return "S-Tier"
	case "2":
		return "A-Tier"
	case "3":
		return "B-Tier"
	}
	return ""
}

func attribution() string {
	return `<p>📊 Source: <a href="https://liquipedia.net/warcraft/">Liquipedia</a> (CC-BY-SA)</p>`
}

// lastUTCAnchor returns the most recent past hh:00 UTC.
func lastUTCAnchor(ref time.Time, hour int) time.Time {
	u := ref.UTC()
	target := time.Date(u.Year(), u.Month(), u.Day(), hour, 0, 0, 0, time.UTC)
	if target.After(u) {
		target = target.Add(-24 * time.Hour)
	}
	return target
}

func dateHuman(t time.Time) string {
	return t.UTC().Format("January 2")
}
