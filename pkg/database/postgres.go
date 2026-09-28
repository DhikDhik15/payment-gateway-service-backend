package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMaxConns        = 25
	defaultMinConns        = 5
	defaultMaxConnLifetime = 5 * time.Minute
	defaultMaxConnIdleTime = 1 * time.Minute
	defaultConnectTimeout  = 10 * time.Second

	// ExpectedMigrationVersion is the minimum clean schema version required
	// by this release. Future compatible migrations may advance beyond it.
	ExpectedMigrationVersion = 19
)

// NewPool creates and validates a new pgxpool connection pool.
// The caller is responsible for closing the pool with pool.Close().
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database config: %w", err)
	}

	config.MaxConns = defaultMaxConns
	config.MinConns = defaultMinConns
	config.MaxConnLifetime = defaultMaxConnLifetime
	config.MaxConnIdleTime = defaultMaxConnIdleTime
	config.ConnConfig.ConnectTimeout = defaultConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	slog.Info("database connection pool established",
		slog.Int("max_conns", int(config.MaxConns)),
		slog.Int("min_conns", int(config.MinConns)),
	)

	return pool, nil
}

// Ping checks that the database is reachable. Useful for readiness probes.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return pool.Ping(ctx)
}

// CheckSchema verifies that the database has a clean migration state at or
// beyond the release's minimum supported version. A reachable database with a
// missing, old, or dirty schema is not ready for application traffic.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("database pool is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var version int
	var dirty bool
	if err := pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		return fmt.Errorf("read schema migration state: %w", err)
	}
	if dirty {
		return fmt.Errorf("database schema migration is dirty at version %d", version)
	}
	if version < ExpectedMigrationVersion {
		return fmt.Errorf("database schema version %d is below required version %d", version, ExpectedMigrationVersion)
	}
	return nil
}
