package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/virusalex/wc3-tracker/internal/config"
	"github.com/virusalex/wc3-tracker/internal/daemon"
	"github.com/virusalex/wc3-tracker/internal/db"
	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/tg"
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

var seedFavorites = []string{"Moon", "Happy", "Lyn", "Infi", "120", "Sok", "Fortitude", "LawLiet", "FoCuS"}

func main() {
	digestOnce := flag.Bool("digest", false, "send one daily digest and exit")
	digestDry := flag.Bool("digest-dry", false, "print the digest HTML to stdout and exit (no send)")
	flag.Parse()
	if *digestDry {
		*digestOnce = true
	}

	_ = godotenv.Load()
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config")
	}
	log.Info().Str("wiki", cfg.Wiki).Int("tierMax", cfg.TierMax).
		Bool("proxy", cfg.ProxyURL != "").Msg("wc3-tracker starting")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	api, err := liquipedia.New(liquipedia.Config{
		APIKey: cfg.APIKey, UserAgent: cfg.UserAgent, Wiki: cfg.Wiki, ProxyURL: cfg.ProxyURL,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("liquipedia client")
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("db open")
	}
	defer store.Close()
	if err := store.Migrate(ctx, seedFavorites); err != nil {
		log.Fatal().Err(err).Msg("db migrate")
	}
	bot := tg.New(cfg.TelegramBotToken, cfg.ChatID)
	var wc *w3c.Client
	if cfg.W3CEnrich {
		wc = w3c.NewWithCache(cfg.W3CCacheFile)
		if err := wc.AddProxies(cfg.W3CProxies...); err != nil {
			log.Warn().Err(err).Msg("w3c proxies not added")
		}
		if !*digestOnce {
			// One-shot digest fills the aka map synchronously on first lookup;
			// an async prewarm would race it and starve the digest of data.
			wc.Prewarm()
		}
	}
	d := daemon.New(cfg, api, store, bot, wc)

	if *digestOnce {
		if *digestDry {
			body, matches, withMMR, err := d.BuildDigest(ctx, time.Now())
			if err != nil {
				log.Fatal().Err(err).Msg("digest")
			}
			log.Info().Int("matches", matches).Int("playersWithMMR", withMMR).Msg("digest dry run")
			os.Stdout.WriteString(body + "\n")
			return
		}
		if err := d.SendDailyDigest(ctx, time.Now()); err != nil {
			log.Fatal().Err(err).Msg("digest")
		}
		return
	}
	if err := d.Run(ctx); err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("daemon exited")
	}
	log.Info().Msg("bye")
}
