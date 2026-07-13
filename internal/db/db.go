// Package db is the SQLite store: posted-match bookkeeping + favorite players.
package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ conn *sql.DB }

// Match is the persisted state of a tracked match.
type Match struct {
	Match2ID          string
	TelegramMessageID *string
	Status            string // upcoming / live / finished
	Team1Score        int
	Team2Score        int
	GamesCount        int
	Finished          bool
	UpdatedAt         time.Time
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) Migrate(ctx context.Context, seedFavorites []string) error {
	const schema = `
CREATE TABLE IF NOT EXISTS matches (
	match2id TEXT PRIMARY KEY,
	telegram_message_id TEXT,
	status TEXT NOT NULL DEFAULT 'upcoming',
	team1_score INTEGER NOT NULL DEFAULT 0,
	team2_score INTEGER NOT NULL DEFAULT 0,
	games_count INTEGER NOT NULL DEFAULT 0,
	finished INTEGER NOT NULL DEFAULT 0,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS favorites (
	name TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS kv (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
);`
	if _, err := d.conn.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Seed favorites only if the table is empty (so manual edits persist).
	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM favorites`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for _, name := range seedFavorites {
			if _, err := d.conn.ExecContext(ctx, `INSERT OR IGNORE INTO favorites(name) VALUES(?)`, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// Favorites returns the lower-cased favorite player names for matching.
func (d *DB) Favorites(ctx context.Context) (map[string]struct{}, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT name FROM favorites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	return out, rows.Err()
}

func (d *DB) Match(ctx context.Context, id string) (*Match, error) {
	var m Match
	var finished int
	err := d.conn.QueryRowContext(ctx, `
		SELECT match2id, telegram_message_id, status, team1_score, team2_score, games_count, finished
		FROM matches WHERE match2id=?`, id).
		Scan(&m.Match2ID, &m.TelegramMessageID, &m.Status, &m.Team1Score, &m.Team2Score, &m.GamesCount, &finished)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Finished = finished == 1
	return &m, nil
}

// Upsert stores the latest known state of a match (without touching the message id).
func (d *DB) Upsert(ctx context.Context, m *Match) error {
	fin := 0
	if m.Finished {
		fin = 1
	}
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO matches(match2id, status, team1_score, team2_score, games_count, finished, updated_at)
		VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(match2id) DO UPDATE SET
			status=excluded.status, team1_score=excluded.team1_score, team2_score=excluded.team2_score,
			games_count=excluded.games_count, finished=excluded.finished, updated_at=CURRENT_TIMESTAMP`,
		m.Match2ID, m.Status, m.Team1Score, m.Team2Score, m.GamesCount, fin)
	return err
}

func (d *DB) SetMessageID(ctx context.Context, id, msgID string) error {
	_, err := d.conn.ExecContext(ctx, `UPDATE matches SET telegram_message_id=? WHERE match2id=?`, msgID, id)
	return err
}

func (d *DB) ClearMessageID(ctx context.Context, id string) error {
	_, err := d.conn.ExecContext(ctx, `UPDATE matches SET telegram_message_id=NULL WHERE match2id=?`, id)
	return err
}

// ActiveMatches returns matches that have been posted and are not yet finished
// (so the daemon keeps editing them).
func (d *DB) ActiveMatches(ctx context.Context) ([]Match, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT match2id, telegram_message_id, status, team1_score, team2_score, games_count, finished
		FROM matches WHERE telegram_message_id IS NOT NULL AND finished=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var m Match
		var finished int
		if err := rows.Scan(&m.Match2ID, &m.TelegramMessageID, &m.Status, &m.Team1Score, &m.Team2Score, &m.GamesCount, &finished); err != nil {
			return nil, err
		}
		m.Finished = finished == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) DeleteOldFinished(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().Add(-age).UTC().Format("2006-01-02 15:04:05")
	res, err := d.conn.ExecContext(ctx, `DELETE FROM matches WHERE finished=1 AND updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteStaleUnfinished drops unfinished matches that stopped updating long
// ago: cancelled or rescheduled matches that left the query window would
// otherwise sit in the active set forever.
func (d *DB) DeleteStaleUnfinished(ctx context.Context, age time.Duration) (int64, error) {
	cutoff := time.Now().Add(-age).UTC().Format("2006-01-02 15:04:05")
	res, err := d.conn.ExecContext(ctx, `DELETE FROM matches WHERE finished=0 AND updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetKV returns the value for a key, or "" when absent.
func (d *DB) GetKV(ctx context.Context, k string) (string, error) {
	var v string
	err := d.conn.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetKV stores a key/value pair.
func (d *DB) SetKV(ctx context.Context, k, v string) error {
	_, err := d.conn.ExecContext(ctx,
		`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}
