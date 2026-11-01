// Package store manages the persistent SQLite state and generated queries.
package store

//go:generate go tool sqlc generate -f ../../sqlc.yaml

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // Register the pure-Go database/sql driver.

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store owns the single SQLite connection pool and its typed queries.
type Store struct {
	SQL       *sql.DB
	Queries   *db.Queries
	InstallID string
}

// Open creates or reopens a database, migrates it, and ensures an installation ID.
func Open(ctx context.Context, filename string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if filename == "" {
		return nil, fmt.Errorf("database path must not be empty")
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolving database path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	// This path comes from the operator's data_dir, never an API or sandbox path.
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- operator-controlled database path
	if err != nil {
		return nil, fmt.Errorf("creating database: %w", err)
	}
	if err = file.Close(); err != nil {
		return nil, fmt.Errorf("closing database file: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	options := url.Values{}
	for _, pragma := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(1)"} {
		options.Add("_pragma", pragma)
	}
	uri.RawQuery = options.Encode()
	pool, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	result, err := initialize(ctx, pool)
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return result, nil
}

func initialize(ctx context.Context, pool *sql.DB) (*Store, error) {
	if err := pool.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connecting database: %w", err)
	}
	source, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("opening migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, pool, source, goose.WithDisableGlobalRegistry(true), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		return nil, fmt.Errorf("configuring migrations: %w", err)
	}
	if _, err = provider.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrating database: %w", err)
	}
	queries := db.New(pool)
	// The insert is atomic across independent Arcana processes opening the same DB.
	candidate, err := ids.New("sbx")
	if err != nil {
		return nil, fmt.Errorf("generating installation ID: %w", err)
	}
	if err = queries.SetMetaIfAbsent(ctx, db.SetMetaIfAbsentParams{Key: "install_id", Value: candidate}); err != nil {
		return nil, fmt.Errorf("saving installation ID: %w", err)
	}
	installID, err := queries.GetMeta(ctx, "install_id")
	if err != nil {
		return nil, fmt.Errorf("reading installation ID: %w", err)
	}
	return &Store{SQL: pool, Queries: queries, InstallID: installID}, nil
}

// Close releases the database resources.
func (s *Store) Close() error { return s.SQL.Close() }
