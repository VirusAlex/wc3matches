package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramBotToken  string
	ChatID            string
	DBPath            string
	APIKey            string
	ProxyURL          string
	UserAgent         string
	Wiki              string
	UpdateInterval    time.Duration
	PostBeforeMinutes int
	TierMax           int           // tiers 1..TierMax count as "top" (1=S,2=A)
	OldMatchAge       time.Duration // delete finished matches older than this
	W3CEnrich         bool          // enrich cards with W3Champions data
	W3CCacheFile      string        // persisted aka-map cache ("" disables)
	W3CProxies        []string      // fallback SOCKS5 egress routes for W3C/FLO
	PrizeCacheFile    string        // persisted prize-pool cache ("" disables)
	HeartbeatFile     string        // touched after each tick, for healthchecks
	AdminChatID       string        // operational alerts go here ("" disables)
}

// Load reads configuration from the environment. The Liquipedia API key comes
// from LIQUIPEDIA_API_KEY, or is read from the file at LIQUIPEDIA_TOKEN_FILE.
func Load() (Config, error) {
	c := Config{
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		ChatID:            os.Getenv("WC3_CHAT_ID"),
		DBPath:            envOr("DB_PATH", "/app/data/wc3.db"),
		APIKey:            os.Getenv("LIQUIPEDIA_API_KEY"),
		ProxyURL:          os.Getenv("LIQUIPEDIA_PROXY"),
		UserAgent:         envOr("LIQUIPEDIA_USER_AGENT", "wc3-tracker/1.0 (https://github.com/VirusAlex/wc3matches; alexey.egupov@norse.bh)"),
		Wiki:              envOr("LIQUIPEDIA_WIKI", "warcraft"),
		UpdateInterval:    time.Duration(intEnv("UPDATE_INTERVAL_SECONDS", 180)) * time.Second,
		PostBeforeMinutes: intEnv("POST_BEFORE_MINUTES", 30),
		TierMax:           intEnv("TIER_MAX", 2),
		OldMatchAge:       time.Duration(intEnv("OLD_MATCH_AGE_HOURS", 12)) * time.Hour,
		W3CEnrich:         boolEnv("W3C_ENRICH", true),
		W3CCacheFile:      envOr("W3C_CACHE_FILE", "/app/data/w3c-cache.json"),
		W3CProxies:        splitList(os.Getenv("W3C_PROXIES")),
		PrizeCacheFile:    envOr("PRIZE_CACHE_FILE", "/app/data/prizes.json"),
		HeartbeatFile:     envOr("HEARTBEAT_FILE", "/app/data/heartbeat"),
		AdminChatID:       os.Getenv("ADMIN_CHAT_ID"),
	}
	if c.APIKey == "" {
		if f := os.Getenv("LIQUIPEDIA_TOKEN_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return c, err
			}
			c.APIKey = strings.TrimSpace(string(b))
		}
	}
	if c.TelegramBotToken == "" {
		return c, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if c.ChatID == "" {
		return c, errors.New("WC3_CHAT_ID is required")
	}
	if c.APIKey == "" {
		return c, errors.New("LIQUIPEDIA_API_KEY or LIQUIPEDIA_TOKEN_FILE is required")
	}
	return c, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// splitList parses a comma-separated env value into trimmed non-empty items.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func boolEnv(k string, def bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func intEnv(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
