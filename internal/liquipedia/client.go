// Package liquipedia is a thin client for the LiquipediaDB v3 API.
//
// It honours Liquipedia's API Terms of Use:
//   - a descriptive User-Agent with contact info (generic UAs are blocked);
//   - gzip request encoding (the API rejects non-gzip clients with HTTP 406);
//   - Authorization: Apikey <key>;
//   - a client-side rate limit (the free plan allows up to 60 requests/hour).
//
// Liquipedia's servers are not directly reachable from our network, so all
// requests go through an optional SOCKS5 proxy (Config.ProxyURL).
//
// Data retrieved is licensed CC-BY-SA 3.0 and must be attributed to Liquipedia.
package liquipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const baseURL = "https://api.liquipedia.net/api/v3"

type Config struct {
	APIKey    string
	UserAgent string // must include contact info
	Wiki      string // e.g. "warcraft"
	ProxyURL  string // optional socks5://host:port
	MinInterval time.Duration // min spacing between requests (rate limit)
}

type Client struct {
	cfg     Config
	httpc   *http.Client
	lastReq time.Time
}

func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" || cfg.UserAgent == "" || cfg.Wiki == "" {
		return nil, fmt.Errorf("liquipedia: APIKey, UserAgent and Wiki are required")
	}
	transport := &http.Transport{}
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		dialer, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("proxy dialer does not support contexts")
		}
		transport.DialContext = cd.DialContext
	}
	if cfg.MinInterval == 0 {
		cfg.MinInterval = 90 * time.Second // stays well under 60/hr
	}
	return &Client{
		cfg:   cfg,
		httpc: &http.Client{Timeout: 45 * time.Second, Transport: transport},
	}, nil
}

// Matches queries the match2 datapoint. conditions uses LiquipediaDB syntax,
// e.g. `[[finished::0]] AND [[date::>2026-06-30 00:00:00]]`. order e.g. "date ASC".
func (c *Client) Matches(ctx context.Context, conditions, order string, limit int) ([]Match, error) {
	q := url.Values{}
	q.Set("wiki", c.cfg.Wiki)
	q.Set("limit", fmt.Sprintf("%d", limit))
	if order != "" {
		q.Set("order", order)
	}
	if conditions != "" {
		q.Set("conditions", conditions)
	}
	q.Set("query", strings.Join([]string{
		"match2id", "pagename", "date", "dateexact", "finished", "winner",
		"walkover", "resulttype", "bestof", "tournament", "parent", "series",
		"game", "patch", "liquipediatier", "liquipediatiertype", "vod", "stream",
		"links", "match2bracketdata", "match2opponents", "match2games",
	}, ","))

	var out MatchResponse
	if err := c.get(ctx, "/match", q, &out); err != nil {
		return nil, err
	}
	if len(out.Error) > 0 {
		return nil, fmt.Errorf("liquipedia api: %s", strings.Join(out.Error, "; "))
	}
	return out.Result, nil
}

// Tournaments queries the tournament datapoint (prize pool is normalized to
// USD by Liquipedia). conditions e.g. `[[pagename::X]] OR [[pagename::Y]]`.
func (c *Client) Tournaments(ctx context.Context, conditions string, limit int) ([]Tournament, error) {
	q := url.Values{}
	q.Set("wiki", c.cfg.Wiki)
	q.Set("limit", fmt.Sprintf("%d", limit))
	if conditions != "" {
		q.Set("conditions", conditions)
	}
	q.Set("query", strings.Join([]string{
		"pagename", "name", "prizepool", "participantsnumber",
		"startdate", "enddate", "liquipediatier",
	}, ","))

	var out TournamentResponse
	if err := c.get(ctx, "/tournament", q, &out); err != nil {
		return nil, err
	}
	if len(out.Error) > 0 {
		return nil, fmt.Errorf("liquipedia api: %s", strings.Join(out.Error, "; "))
	}
	return out.Result, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	c.throttle(ctx)
	u := baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Authorization", "Apikey "+c.cfg.APIKey)
	// Don't set Accept-Encoding by hand: Go's transport adds gzip itself
	// (satisfying Liquipedia's gzip requirement) and decompresses transparently.
	// Setting it manually would leave us with raw gzip bytes.

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		// Error bodies echo the API key back; never let it reach the logs.
		return fmt.Errorf("liquipedia http %d: %s", resp.StatusCode,
			strings.ReplaceAll(snippet(body), c.cfg.APIKey, "<apikey>"))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w (raw: %s)", err, snippet(body))
	}
	return nil
}

// throttle enforces a minimum spacing between requests to respect the rate limit.
func (c *Client) throttle(ctx context.Context) {
	if c.lastReq.IsZero() {
		c.lastReq = time.Now()
		return
	}
	wait := c.cfg.MinInterval - time.Since(c.lastReq)
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	c.lastReq = time.Now()
}

func snippet(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
