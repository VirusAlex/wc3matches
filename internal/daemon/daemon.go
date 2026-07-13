// Package daemon runs the reconciliation loop: poll Liquipedia for WarCraft III
// matches, post cards for interesting ones starting soon / live, and edit them
// as the series score and maps come in.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/virusalex/wc3-tracker/internal/card"
	"github.com/virusalex/wc3-tracker/internal/config"
	"github.com/virusalex/wc3-tracker/internal/db"
	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/tg"
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

type Daemon struct {
	cfg    config.Config
	api    *liquipedia.Client
	store  *db.DB
	bot    *tg.Bot
	wc     *w3c.Client           // nil = W3Champions enrichment disabled
	prizes map[string]prizeEntry // tournament pagename -> prize pool (USD)
	prizeFailAt time.Time        // last failed lookup; backs off retries
	tickFails   int              // consecutive Matches-query failures
	alertAt     time.Time        // last admin alert (cooldown)
}

// prizeEntry caches a tournament prize-pool lookup; usd 0 = known miss.
type prizeEntry struct {
	usd float64
	at  time.Time
}

func New(cfg config.Config, api *liquipedia.Client, store *db.DB, bot *tg.Bot, wc *w3c.Client) *Daemon {
	d := &Daemon{cfg: cfg, api: api, store: store, bot: bot, wc: wc,
		prizes: map[string]prizeEntry{}}
	d.loadPrizes()
	return d
}

// prizeFile is the on-disk shape of the prize cache. The tournament table has
// a tiny API quota, so lookups must survive restarts.
type prizeFile struct {
	USD float64   `json:"usd"`
	At  time.Time `json:"at"`
}

func (d *Daemon) loadPrizes() {
	if d.cfg.PrizeCacheFile == "" {
		return
	}
	data, err := os.ReadFile(d.cfg.PrizeCacheFile)
	if err != nil {
		return // no cache yet
	}
	var raw map[string]prizeFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for k, v := range raw {
		d.prizes[k] = prizeEntry{usd: v.USD, at: v.At}
	}
	log.Info().Int("tournaments", len(raw)).Msg("prize cache loaded")
}

func (d *Daemon) savePrizes() {
	if d.cfg.PrizeCacheFile == "" {
		return
	}
	raw := make(map[string]prizeFile, len(d.prizes))
	for k, v := range d.prizes {
		raw[k] = prizeFile{USD: v.usd, At: v.at}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	if err := os.WriteFile(d.cfg.PrizeCacheFile, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("prize cache write failed")
	}
}

// resolvePrizes fetches prize pools for tournaments of the given matches that
// are not freshly cached, in one batched query (the Liquipedia rate budget is
// tight, so never one call per tournament). Runs on the daemon goroutine only.
func (d *Daemon) resolvePrizes(ctx context.Context, ms []liquipedia.Match) {
	// The tournament table has its own (small) API quota: after a failure,
	// notably a 429, stay away for a while instead of retrying every tick.
	if time.Since(d.prizeFailAt) < 45*time.Minute {
		return
	}
	var want []string
	requested := map[string]bool{}
	for _, m := range ms {
		p := tournamentPage(m)
		if p == "" || requested[p] {
			continue
		}
		if e, ok := d.prizes[p]; ok {
			maxAge := 12 * time.Hour // retry misses twice a day
			if e.usd > 0 {
				maxAge = 24 * time.Hour // crowdfunded pools grow; refresh daily
			}
			if time.Since(e.at) < maxAge {
				continue
			}
		}
		requested[p] = true
		want = append(want, p)
	}
	if len(want) == 0 {
		return
	}
	conds := make([]string, len(want))
	for i, p := range want {
		conds[i] = "[[pagename::" + p + "]]"
	}
	ts, err := d.api.Tournaments(ctx, strings.Join(conds, " OR "), len(want)+5)
	if err != nil {
		d.prizeFailAt = time.Now()
		log.Warn().Err(err).Msg("tournament prize lookup failed; backing off")
		return
	}
	now := time.Now()
	for _, p := range want {
		d.prizes[p] = prizeEntry{at: now} // miss unless overwritten below
	}
	found := 0
	for _, t := range ts {
		if t.PrizePool > 0 {
			found++
		}
		d.prizes[t.Pagename] = prizeEntry{usd: t.PrizePool, at: now}
	}
	d.savePrizes()
	log.Info().Int("requested", len(want)).Int("withPrize", found).Msg("tournament prizes resolved")
}

// prizeUSD returns the cached prize pool for a match's tournament (0 unknown).
func (d *Daemon) prizeUSD(m liquipedia.Match) float64 {
	return d.prizes[tournamentPage(m)].usd
}

// tournamentPage is the tournament pagename a match belongs to.
func tournamentPage(m liquipedia.Match) string {
	if m.PageName != "" {
		return m.PageName
	}
	return m.Parent
}

// enrich fetches optional W3Champions data for a card render (nil-safe).
func (d *Daemon) enrich(ctx context.Context, m liquipedia.Match) *w3c.Enrichment {
	if d.wc == nil {
		return nil
	}
	return d.wc.EnrichMatch(ctx, m)
}

// playerStats resolves ladder stats only (digest rows; nil-safe).
func (d *Daemon) playerStats(ctx context.Context, m liquipedia.Match) []*w3c.PlayerStats {
	if d.wc == nil {
		return nil
	}
	return d.wc.PlayerStatsFor(ctx, m)
}

// digestHour is when the daily digest goes out (UTC).
const digestHour = 7

func (d *Daemon) Run(ctx context.Context) error {
	log.Info().Dur("interval", d.cfg.UpdateInterval).Msg("daemon starting tick loop")
	tick := time.NewTicker(d.cfg.UpdateInterval)
	defer tick.Stop()
	digestTimer := time.NewTimer(d.digestDelay(ctx))
	defer digestTimer.Stop()

	d.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			d.tick(ctx)
		case <-digestTimer.C:
			if d.runDigest(ctx) {
				digestTimer.Reset(time.Until(nextAtUTC(time.Now(), digestHour, 0)))
			} else {
				digestTimer.Reset(15 * time.Minute) // retry, don't skip the day
			}
		}
	}
}

// digestDelay returns how long to wait before the next digest. If the daemon
// was down (or failed) across today's slot, catch up right away.
func (d *Daemon) digestDelay(ctx context.Context) time.Duration {
	anchor := lastUTCAnchor(time.Now(), digestHour)
	sent, err := d.store.GetKV(ctx, "digest_sent")
	if err == nil && sent != anchor.Format("2006-01-02") {
		log.Info().Str("missed", anchor.Format("2006-01-02")).Msg("digest catch-up scheduled")
		return 15 * time.Second
	}
	return time.Until(nextAtUTC(time.Now(), digestHour, 0))
}

// runDigest sends the daily digest, records success and alerts on failure.
func (d *Daemon) runDigest(ctx context.Context) bool {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := d.SendDailyDigest(dctx, time.Now()); err != nil {
		log.Error().Err(err).Msg("digest failed")
		d.alert(ctx, "daily digest failed: "+err.Error()+" (retrying in 15m)")
		return false
	}
	anchor := lastUTCAnchor(time.Now(), digestHour)
	if err := d.store.SetKV(ctx, "digest_sent", anchor.Format("2006-01-02")); err != nil {
		log.Error().Err(err).Msg("digest_sent bookkeeping failed")
	}
	return true
}

// alert sends an operational warning to the admin chat (no-op when unset).
// Never posts to the public channel.
func (d *Daemon) alert(ctx context.Context, text string) {
	if d.cfg.AdminChatID == "" {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := d.bot.SendTextTo(actx, d.cfg.AdminChatID, "⚠️ wc3-tracker: "+text); err != nil {
		log.Error().Err(err).Msg("admin alert failed")
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
	// Cancelled / rescheduled matches that left the query window stop being
	// updated and would sit in the active set forever.
	if n, _ := d.store.DeleteStaleUnfinished(tickCtx, 48*time.Hour); n > 0 {
		log.Info().Int64("deleted", n).Msg("cleaned up stale unfinished matches")
	}

	now := time.Now().UTC()
	lo := now.Add(-12 * time.Hour).Format("2006-01-02 15:04:05")
	hi := now.Add(72 * time.Hour).Format("2006-01-02 15:04:05")
	cond := "[[date::>" + lo + "]] AND [[date::<" + hi + "]]"
	matches, err := d.api.Matches(tickCtx, cond, "date ASC", 100)
	if err != nil {
		log.Error().Err(err).Msg("Matches query failed")
		d.tickFails++
		// A stuck data source is invisible in the channel; tell the admin.
		if d.tickFails == 5 && time.Since(d.alertAt) > 6*time.Hour {
			d.alertAt = time.Now()
			d.alert(ctx, "Liquipedia queries failing for 5 consecutive ticks: "+err.Error())
		}
		return
	}
	d.tickFails = 0

	// Index interesting matches by id for the edit phase.
	fresh := map[string]liquipedia.Match{}
	var interestingList []liquipedia.Match
	for _, m := range matches {
		if interesting(m, favorites, d.cfg.TierMax) {
			interestingList = append(interestingList, m)
		}
	}
	d.resolvePrizes(tickCtx, interestingList)

	posted, edited, unchanged := 0, 0, 0
	for _, m := range interestingList {
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
			// Still record the state: without this a match whose final edit
			// happened earlier never gets marked finished (and updated_at
			// would go stale on healthy matches, tripping the zombie sweep).
			_ = d.store.Upsert(tickCtx, matchToDB(m))
		case errors.Is(err, tg.ErrMessageNotEditable):
			_ = d.store.ClearMessageID(tickCtx, mr.Match2ID)
		default:
			log.Error().Err(err).Str("match", mr.Match2ID).Msg("edit failed")
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Info().Int("scanned", len(matches)).Int("interesting", len(fresh)).
		Int("posted", posted).Int("edited", edited).Int("unchanged", unchanged).Msg("tick done")
	d.heartbeat()
}

// heartbeat touches a file after every completed tick; the container
// healthcheck compares its mtime against the tick interval.
func (d *Daemon) heartbeat() {
	if d.cfg.HeartbeatFile == "" {
		return
	}
	if err := os.WriteFile(d.cfg.HeartbeatFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		log.Warn().Err(err).Msg("heartbeat write failed")
	}
}

func (d *Daemon) postCard(ctx context.Context, m liquipedia.Match) error {
	text := card.RenderExtras(m, time.Now(), &card.Extras{W3C: d.enrich(ctx, m), PrizeUSD: d.prizeUSD(m)})
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
	text := card.RenderExtras(m, time.Now(), &card.Extras{W3C: d.enrich(ctx, m), PrizeUSD: d.prizeUSD(m)})
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
	// dateexact==0 is a day-only placeholder (midnight): the date passing means
	// nothing, so post only on real evidence of the match being live.
	if m.DateExact == 0 {
		return m.LiveEvidence()
	}
	if !t.After(now.UTC()) {
		return true // already started
	}
	return t.Before(now.UTC().Add(time.Duration(beforeMin) * time.Minute))
}

func startedUTC(m liquipedia.Match, now time.Time) bool {
	if m.DateExact == 0 {
		return m.LiveEvidence()
	}
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
