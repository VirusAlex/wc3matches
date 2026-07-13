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
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

// SendDailyDigest posts a schedule of interesting matches for the next 24h,
// grouped by tournament. Exact-timed matches show their time; matches with a
// floating bracket time are listed without a time (they'll appear live).
func (d *Daemon) SendDailyDigest(ctx context.Context, ref time.Time) error {
	body, matches, withMMR, err := d.BuildDigest(ctx, ref)
	if err != nil {
		return err
	}
	// The send gets its own deadline: enrichment inside BuildDigest may have
	// eaten the caller's budget, and a built digest must still go out.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if _, err := d.bot.SendRich(sendCtx, body); err != nil {
		return err
	}
	log.Info().Int("matches", matches).Int("playersWithMMR", withMMR).Msg("daily digest sent")
	return nil
}

// BuildDigest renders the digest body without sending it. Returns the HTML,
// the number of listed matches and how many players resolved a W3C MMR.
func (d *Daemon) BuildDigest(ctx context.Context, ref time.Time) (string, int, int, error) {
	favorites, err := d.store.Favorites(ctx)
	if err != nil {
		return "", 0, 0, err
	}
	start := lastUTCAnchor(ref, 7)
	end := start.Add(24 * time.Hour)
	cond := fmt.Sprintf("[[finished::0]] AND [[date::>%s]] AND [[date::<%s]]",
		start.UTC().Format("2006-01-02 15:04:05"), end.UTC().Format("2006-01-02 15:04:05"))
	matches, err := d.api.Matches(ctx, cond, "date ASC", 100)
	if err != nil {
		return "", 0, 0, err
	}

	var interestingMatches []liquipedia.Match
	for _, m := range matches {
		if interesting(m, favorites, d.cfg.TierMax) {
			interestingMatches = append(interestingMatches, m)
		}
	}

	title := "📅 WarCraft III matches for " + dateHuman(start)
	if len(interestingMatches) == 0 {
		return fmt.Sprintf("<h3>%s</h3><p>No notable matches 🤷</p>"+attribution(), title), 0, 0, nil
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

	d.resolvePrizes(ctx, interestingMatches)

	// Resolve W3Champions MMR for every listed player up front (cached). MMR
	// is optional garnish: when the API is slow, stop looking things up while
	// there is still budget left to render and send the digest itself.
	statsBy := map[string][]*w3c.PlayerStats{}
	withMMR := 0
	for i, m := range interestingMatches {
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < 90*time.Second {
			log.Warn().Int("resolved", i).Int("total", len(interestingMatches)).
				Msg("digest MMR lookups truncated: running out of time")
			break
		}
		st := d.playerStats(ctx, m)
		statsBy[m.Match2ID] = st
		for _, s := range st {
			if s != nil && s.MMR > 0 {
				withMMR++
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<h3>%s</h3>", title)
	if withMMR > 0 {
		b.WriteString("<p><i>Race · W3Champions MMR in brackets</i></p>")
	}
	for _, ev := range order {
		ms := byEvent[ev]
		label := ev
		if t := tierShort(ms[0].LiquipediaTier); t != "" {
			label += " · " + t
		}
		if p := d.prizeUSD(ms[0]); p > 0 {
			label += " · 💰 " + card.FormatUSD(p)
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
					t.UTC().Format("15:04"), pairing(m, statsBy[m.Match2ID]), m.BestOf)
			}
			b.WriteString("</table>")
		}
		if len(floating) > 0 {
			b.WriteString("<p><i>🕐 Time TBD (will appear live):</i></p>")
			seen := map[string]bool{}
			for _, m := range floating {
				p := pairing(m, statsBy[m.Match2ID])
				if seen[p] {
					continue
				}
				seen[p] = true
				fmt.Fprintf(&b, "<p>• %s</p>", p)
			}
		}
	}
	b.WriteString(attribution())
	return b.String(), len(interestingMatches), withMMR, nil
}

// pairing renders "🇰🇷 Moon (Night Elf · 2650) vs 🇨🇳 Infi (Human · 2597)" for a
// digest row; the number is the player's W3Champions MMR when known.
func pairing(m liquipedia.Match, stats []*w3c.PlayerStats) string {
	side := func(i int) string {
		if i >= len(m.Opponents) {
			return "TBD"
		}
		o := m.Opponents[i]
		s := html.EscapeString(o.Display())
		if e := card.FlagEmoji(o.Flag()); e != "" {
			s = e + " " + s
		}
		var parts []string
		if r := card.FactionName(o.Faction()); r != "" {
			parts = append(parts, r)
		}
		if i < len(stats) && stats[i] != nil && stats[i].MMR > 0 {
			parts = append(parts, fmt.Sprintf("%d", stats[i].MMR))
		}
		if len(parts) > 0 {
			s += " <i>(" + strings.Join(parts, " · ") + ")</i>"
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
