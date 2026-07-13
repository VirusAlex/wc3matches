# wc3-tracker

[![Channel](https://img.shields.io/badge/Telegram-%40wc3matches-26A5E4?logo=telegram&logoColor=white)](https://t.me/wc3matches)
[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Data](https://img.shields.io/badge/data-Liquipedia%20CC--BY--SA%203.0-blue)](https://liquipedia.net/warcraft/)

A Telegram bot that posts and live-updates **WarCraft III** match cards from
[Liquipedia](https://liquipedia.net/warcraft/). It tracks top-tier (S/A) matches
and matches featuring favourite players, posting a rich card before the match
and editing it live as the series score and maps come in.

**Live channel:** [t.me/wc3matches](https://t.me/wc3matches)

Sibling project of an HLTV (CS2) tracker; shares the same Telegram rich-message
rendering (Bot API 10.1: tables, headings, collapsible map sections, images).

## Data source & attribution

All match data comes from the **LiquipediaDB API**. Liquipedia content is
licensed under **[CC-BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0/)**.
Every message credits Liquipedia and links back to the source page, as required.
This project is provided as open source per Liquipedia's API terms.

We honour the API terms of use:

- a descriptive `User-Agent` with contact info;
- gzip transport;
- rate limited well under the free plan's 60 requests/hour;
- results are cached (SQLite); no scraping of rendered HTML pages.

## Features

- Cards for upcoming / live / finished 1v1 matches: players (country flag +
  race), Bo, tier, game (Reforged/TFT), start time (UTC), tournament.
- Series score + 🏆 winner in the title, updated live.
- Per-map collapsible sections: map thumbnail + a Player / Race / Heroes table.
- [W3Champions](https://w3champions.com) enrichment (optional, best-effort):
  ladder MMR + league rank for both players, and per-map duration, final hero
  levels and unit counts when the game itself went through W3C matchmaking.
  Players are matched via the aka data W3C publishes in its league rankings.
- [FLO](https://w3flo.com) enrichment: tournament games hosted in FLO lobbies
  (invisible to the matchmaking API) get live "in progress" markers, per-game
  races and map durations while they are in FLO's recent-games window.
- Bracket stage in the card ("Grand Final", "Lower Bracket Round 2", ...) and
  the tournament prize pool in USD (from the LiquipediaDB tournament table).
- Operational hardening: heartbeat-based Docker healthcheck, optional alerts
  to an admin chat (`ADMIN_CHAT_ID`), daily digest with retry and catch-up
  after downtime, persisted API caches to respect the providers' rate limits.
- Watch (stream) / VOD / Head-to-Head links when available.
- Daily digest grouped by tournament.
- Favourites + tier filter (configurable).

## Configuration

Copy `.env.example` to `.env` and fill it in. Key variables:

| Var | Meaning |
|-----|---------|
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `WC3_CHAT_ID` | target channel/chat id |
| `LIQUIPEDIA_API_KEY` / `LIQUIPEDIA_TOKEN_FILE` | LiquipediaDB API key (or file path) |
| `LIQUIPEDIA_PROXY` | `socks5://host:port` (Liquipedia is reached via proxy) |
| `TIER_MAX` | tiers `1..N` count as "top" (1=S, 2=A) |
| `W3C_ENRICH` | `1`/`0` — W3Champions enrichment (default on) |

Favourite players are seeded into the `favorites` table on first run and can be
edited via SQL afterwards.

## Run

```sh
cp .env.example .env   # fill in token + chat id
# put your Liquipedia API key in ./.liquipedia.token (git-ignored)
docker compose up -d --build
```

The container uses host networking so it can reach the SOCKS5 proxy on the host.

## Licence

Code: MIT (see `LICENSE`). Data: © Liquipedia contributors, CC-BY-SA 3.0.
