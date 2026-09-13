package studio

import (
	"context"
	"sync/atomic"
	"time"

	"t3z/api-gateway/internal/database"
)

type sqliteLimits struct {
	db       *database.DB
	requests atomic.Uint64
}

func newSQLiteLimits(db *database.DB) (*sqliteLimits, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS studio_rate_limits (key TEXT PRIMARY KEY, started_at INTEGER NOT NULL, count INTEGER NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return &sqliteLimits{db: db}, nil
}

func (limits *sqliteLimits) Take(ctx context.Context, key string, maximum int, duration time.Duration) error {
	now := time.Now().UnixMilli()
	if limits.requests.Add(1)%100 == 0 {
		_, _ = limits.db.ExecContext(ctx, `DELETE FROM studio_rate_limits WHERE started_at < ?`, now-int64(24*time.Hour/time.Millisecond))
	}
	var count int
	err := limits.db.QueryRowContext(ctx, `
		INSERT INTO studio_rate_limits(key, started_at, count) VALUES (?, ?, 1)
		ON CONFLICT(key) DO UPDATE SET
		count = CASE WHEN started_at <= ? THEN 1 ELSE count + 1 END,
		started_at = CASE WHEN started_at <= ? THEN excluded.started_at ELSE started_at END
		RETURNING count`, key, now, now-duration.Milliseconds(), now-duration.Milliseconds()).Scan(&count)
	if err != nil {
		return err
	}
	if count > maximum {
		return fail(429, "Too many requests. Try again later.")
	}
	return nil
}
