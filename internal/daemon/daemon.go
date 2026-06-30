// Package daemon runs the reconciliation loop: poll Liquipedia for WarCraft III
// matches, post cards for interesting ones starting soon / live, and edit them
// as the series score and maps come in.
package daemon

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/virusalex/wc3-tracker/internal/card"
	"github.com/virusalex/wc3-tracker/internal/config"
	"github.com/virusalex/wc3-tracker/internal/db"
	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/tg"
)

type Daemon struct {
	cfg   config.Config
	api   *liquipedia.Client
	store *db.DB
	bot   *tg.Bot
}

func New(cfg config.Config, api *liquipedia.Client, store *db.DB, bot *tg.Bot) *Daemon {
	return &Daemon{cfg: cfg, api: api, store: store, bot: bot}
}

func (d *Daemon) Run(ctx context.Context) error {
	log.Info().Dur("interval", d.cfg.UpdateInterval).Msg("daemon starting tick loop")
	tick := time.NewTicker(d.cfg.UpdateInterval)
	defer tick.Stop()
	digestTimer := time.NewTimer(time.Until(nextAtUTC(time.Now(), 7, 0)))
	defer digestTimer.Stop()

	d.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			d.tick(ctx)
		case <-digestTimer.C:
			if err := d.SendDailyDigest(ctx, time.Now()); err != nil {
				log.Error().Err(err).Msg("digest failed")
			}
			digestTimer.Reset(time.Until(nextAtUTC(time.Now(), 7, 0)))
		}
	}
}

func (d *Daemon) tick(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	favorites, err := d.store.Favorites(tickCtx)
	if err != nil {
		log.Error().Err(err).Msg("load favorites; skipping tick")
		return
	}
	if d.cfg.OldMatchAge > 0 {
		if n, _ := d.store.DeleteOldFinished(tickCtx, d.cfg.OldMatchAge); n > 0 {
			log.Info().Int64("deleted", n).Msg("cleaned up old matches")
		}
	}

	now := time.Now().UTC()
	lo := now.Add(-12 * time.Hour).Format("2006-01-02 15:04:05")
	hi := now.Add(72 * time.Hour).Format("2006-01-02 15:04:05")
	cond := "[[date::>" + lo + "]] AND [[date::<" + hi + "]]"
	matches, err := d.api.Matches(tickCtx, cond, "date ASC", 100)
	if err != nil {
		log.Error().Err(err).Msg("Matches query failed")
		return
	}

	// Index interesting matches by id for the edit phase.
	fresh := map[string]liquipedia.Match{}
	posted, edited, unchanged := 0, 0, 0
	for _, m := range matches {
		if !interesting(m, favorites, d.cfg.TierMax) {
			continue
		}
		fresh[m.Match2ID] = m

		existing, err := d.store.Match(tickCtx, m.Match2ID)
		if err != nil {
			log.Error().Err(err).Str("match", m.Match2ID).Msg("db lookup")
			continue
		}
		if existing != nil && existing.TelegramMessageID != nil {
			continue // already posted; edit phase handles it
		}
		if !shouldPost(m, time.Now(), d.cfg.PostBeforeMinutes) {
			continue
		}
		if err := d.postCard(tickCtx, m); err != nil {
			log.Error().Err(err).Str("match", m.Match2ID).Msg("post failed")
			continue
		}
		posted++
		time.Sleep(500 * time.Millisecond)
	}

	// Edit phase: refresh already-posted, unfinished matches.
	active, err := d.store.ActiveMatches(tickCtx)
	if err != nil {
		log.Error().Err(err).Msg("active matches")
		return
	}
	for _, mr := range active {
		m, ok := fresh[mr.Match2ID]
		if !ok {
			continue // outside window or no longer interesting; leave as-is
		}
		err := d.editCard(tickCtx, mr, m)
		switch {
		case err == nil:
			edited++
		case errors.Is(err, tg.ErrNotModified):
			unchanged++
		case errors.Is(err, tg.ErrMessageNotEditable):
			_ = d.store.ClearMessageID(tickCtx, mr.Match2ID)
		default:
			log.Error().Err(err).Str("match", mr.Match2ID).Msg("edit failed")
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Info().Int("scanned", len(matches)).Int("interesting", len(fresh)).
		Int("posted", posted).Int("edited", edited).Int("unchanged", unchanged).Msg("tick done")
}

func (d *Daemon) postCard(ctx context.Context, m liquipedia.Match) error {
	text := card.Render(m, time.Now())
	msgID, err := d.bot.SendRich(ctx, text)
	if err != nil {
		return err
	}
	if err := d.store.Upsert(ctx, matchToDB(m)); err != nil {
		return err
	}
	if err := d.store.SetMessageID(ctx, m.Match2ID, strconv.FormatInt(msgID, 10)); err != nil {
		return err
	}
	log.Info().Str("match", m.Match2ID).Int64("msg", msgID).
		Str("teams", playersLabel(m)).Msg("posted card")
	return nil
}

func (d *Daemon) editCard(ctx context.Context, mr db.Match, m liquipedia.Match) error {
	if mr.TelegramMessageID == nil {
		return nil
	}
	msgID, err := strconv.ParseInt(*mr.TelegramMessageID, 10, 64)
	if err != nil {
		return err
	}
	text := card.Render(m, time.Now())
	if err := d.bot.EditRich(ctx, msgID, text); err != nil {
		return err
	}
	return d.store.Upsert(ctx, matchToDB(m))
}

func matchToDB(m liquipedia.Match) *db.Match {
	t1, t2 := 0, 0
	if len(m.Opponents) > 0 && m.Opponents[0].Score > 0 {
		t1 = m.Opponents[0].Score
	}
	if len(m.Opponents) > 1 && m.Opponents[1].Score > 0 {
		t2 = m.Opponents[1].Score
	}
	status := "upcoming"
	if m.Finished == 1 {
		status = "finished"
	} else if startedUTC(m, time.Now()) {
		status = "live"
	}
	return &db.Match{
		Match2ID:   m.Match2ID,
		Status:     status,
		Team1Score: t1,
		Team2Score: t2,
		GamesCount: len(m.Games),
		Finished:   m.Finished == 1,
	}
}

// interesting: a top-tier match (tier <= TierMax) or one featuring a favorite.
// Requires both opponents to be known (no TBD bracket slots).
func interesting(m liquipedia.Match, favorites map[string]struct{}, tierMax int) bool {
	if len(m.Opponents) < 2 || m.Opponents[0].Display() == "TBD" || m.Opponents[1].Display() == "TBD" {
		return false
	}
	if tier, err := strconv.Atoi(m.LiquipediaTier); err == nil && tier >= 1 && tier <= tierMax {
		return true
	}
	for _, o := range m.Opponents {
		for _, p := range o.Players {
			name := strings.ToLower(strings.TrimSpace(p.DisplayName))
			if name == "" {
				name = strings.ToLower(strings.TrimSpace(p.Name))
			}
			if _, ok := favorites[name]; ok && name != "" {
				return true
			}
		}
	}
	return false
}

// shouldPost: live (started, unfinished) or about to start within the window.
func shouldPost(m liquipedia.Match, now time.Time, beforeMin int) bool {
	if m.Finished == 1 {
		return false
	}
	t, err := time.Parse("2006-01-02 15:04:05", m.Date)
	if err != nil {
		return false
	}
	if !t.After(now.UTC()) {
		return true // already started
	}
	// dateexact==0 means time is a placeholder; don't pre-post, wait for live.
	if m.DateExact == 0 {
		return false
	}
	return t.Before(now.UTC().Add(time.Duration(beforeMin) * time.Minute))
}

func startedUTC(m liquipedia.Match, now time.Time) bool {
	t, err := time.Parse("2006-01-02 15:04:05", m.Date)
	if err != nil {
		return false
	}
	return !t.After(now.UTC())
}

func playersLabel(m liquipedia.Match) string {
	if len(m.Opponents) < 2 {
		return m.Match2ID
	}
	return m.Opponents[0].Display() + " vs " + m.Opponents[1].Display()
}

// nextAtUTC returns the next occurrence of hh:mm UTC.
func nextAtUTC(now time.Time, hh, mm int) time.Time {
	u := now.UTC()
	target := time.Date(u.Year(), u.Month(), u.Day(), hh, mm, 0, 0, time.UTC)
	if !target.After(u) {
		target = target.Add(24 * time.Hour)
	}
	return target
}
