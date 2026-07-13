package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/virusalex/wc3-tracker/internal/card"
	"github.com/virusalex/wc3-tracker/internal/liquipedia"
	"github.com/virusalex/wc3-tracker/internal/tg"
	"github.com/virusalex/wc3-tracker/internal/w3c"
)

func main() {
	tokenFile := flag.String("token", "/opt/docker/hltv-tracker/.liquipedia.token", "API key file")
	proxyURL := flag.String("proxy", "socks5://127.0.0.1:18237", "socks5 proxy")
	cond := flag.String("cond", "", "conditions override")
	limit := flag.Int("limit", 5, "max matches to fetch")
	player := flag.String("player", "", "only matches featuring this player (substring, case-insensitive)")
	withW3C := flag.Bool("w3c", false, "enrich cards with W3Champions ladder stats")
	post := flag.Bool("post", false, "post rendered cards to Telegram (requires -env and -chat)")
	envPath := flag.String("env", "", "env file with TELEGRAM_BOT_TOKEN (required with -post)")
	chatID := flag.String("chat", "", "chat id to post to (required with -post; never a production channel)")
	flag.Parse()

	key, err := os.ReadFile(*tokenFile)
	if err != nil {
		die("read token: %v", err)
	}
	c, err := liquipedia.New(liquipedia.Config{
		APIKey:      strings.TrimSpace(string(key)),
		UserAgent:   "wc3-tracker/0.1 (https://github.com/virusalex/wc3-tracker; alexey.egupov@norse.bh)",
		Wiki:        "warcraft",
		ProxyURL:    *proxyURL,
		MinInterval: time.Second,
	})
	if err != nil {
		die("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var matches []liquipedia.Match
	if *cond != "" {
		matches, err = c.Matches(ctx, *cond, "date ASC", *limit)
	} else {
		// one finished (with games) + a couple recent finished as samples
		matches, err = c.Matches(ctx, "[[finished::1]] AND [[bestof::>1]]", "date DESC", 3)
	}
	if err != nil {
		die("matches: %v", err)
	}
	if *player != "" {
		matches = filterByPlayer(matches, *player)
	}
	fmt.Printf("got %d matches\n", len(matches))

	var bot *tg.Bot
	if *post {
		if *envPath == "" || *chatID == "" {
			die("-post requires explicit -env and -chat (no defaults: prod channels are off-limits)")
		}
		env := readEnv(*envPath)
		if env["TELEGRAM_BOT_TOKEN"] == "" {
			die("no TELEGRAM_BOT_TOKEN in %s", *envPath)
		}
		bot = tg.New(env["TELEGRAM_BOT_TOKEN"], *chatID)
	}
	var wc *w3c.Client
	if *withW3C {
		wc = w3c.New()
	}
	prizes := lookupPrizes(ctx, c, matches)
	now := time.Now()
	for _, m := range matches {
		var enrichment *w3c.Enrichment
		if wc != nil {
			enrichment = wc.EnrichMatch(ctx, m)
		}
		htmlBody := card.RenderExtras(m, now, &card.Extras{W3C: enrichment, PrizeUSD: prizes[m.PageName]})
		fmt.Println("\n----", m.Match2ID, "----")
		fmt.Println(htmlBody)
		if bot != nil {
			// Own context: enrichment may have eaten most of the main one.
			sendCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			id, err := bot.SendRich(sendCtx, htmlBody)
			cancel()
			fmt.Printf("posted id=%d err=%v\n", id, err)
			time.Sleep(800 * time.Millisecond)
		}
	}
}

// lookupPrizes fetches prize pools for the matches' tournaments in one query.
func lookupPrizes(ctx context.Context, c *liquipedia.Client, matches []liquipedia.Match) map[string]float64 {
	out := map[string]float64{}
	var conds []string
	for _, m := range matches {
		if m.PageName != "" && out[m.PageName] == 0 {
			out[m.PageName] = -1 // mark requested
			conds = append(conds, "[[pagename::"+m.PageName+"]]")
		}
	}
	if len(conds) == 0 {
		return out
	}
	ts, err := c.Tournaments(ctx, strings.Join(conds, " OR "), len(conds)+5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prize lookup: %v\n", err)
		return map[string]float64{}
	}
	for k := range out {
		out[k] = 0
	}
	for _, t := range ts {
		out[t.Pagename] = t.PrizePool
	}
	return out
}

func filterByPlayer(matches []liquipedia.Match, sub string) []liquipedia.Match {
	sub = strings.ToLower(sub)
	var out []liquipedia.Match
	for _, m := range matches {
		for _, o := range m.Opponents {
			if strings.Contains(strings.ToLower(o.Display()), sub) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

func readEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return out
}

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
