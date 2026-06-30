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
)

func main() {
	tokenFile := flag.String("token", "/opt/docker/hltv-tracker/.liquipedia.token", "API key file")
	proxyURL := flag.String("proxy", "socks5://127.0.0.1:18237", "socks5 proxy")
	cond := flag.String("cond", "", "conditions override")
	post := flag.Bool("post", false, "post rendered cards to Telegram (reuses hltv .env)")
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var matches []liquipedia.Match
	if *cond != "" {
		matches, err = c.Matches(ctx, *cond, "date ASC", 5)
	} else {
		// one finished (with games) + a couple recent finished as samples
		matches, err = c.Matches(ctx, "[[finished::1]] AND [[bestof::>1]]", "date DESC", 3)
	}
	if err != nil {
		die("matches: %v", err)
	}
	fmt.Printf("got %d matches\n", len(matches))

	var bot *tg.Bot
	if *post {
		env := readEnv("/opt/docker/hltv-tracker/.env")
		bot = tg.New(env["TELEGRAM_BOT_TOKEN"], envOr(env, "CS2_CHAT_ID", "-1003628692578"))
	}
	now := time.Now()
	for _, m := range matches {
		htmlBody := card.Render(m, now)
		fmt.Println("\n----", m.Match2ID, "----")
		fmt.Println(htmlBody)
		if bot != nil {
			id, err := bot.SendRich(ctx, htmlBody)
			fmt.Printf("posted id=%d err=%v\n", id, err)
			time.Sleep(800 * time.Millisecond)
		}
	}
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

func envOr(m map[string]string, k, def string) string {
	if v := m[k]; v != "" {
		return v
	}
	return def
}

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
