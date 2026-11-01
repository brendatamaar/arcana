package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/brendatamaar/arcana/internal/store/db"
)

// Bootstrap creates an admin only when no key has ever been created.
// The database transaction serializes the decision. File creation is exclusive:
// an unexpected existing file is an operator error, never silently overwritten.
func (s *Service) Bootstrap(ctx context.Context, dataDir string, out io.Writer) error {
	tx, err := s.store.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("starting bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire SQLite's writer lock before observing the bootstrap condition.
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=value WHERE key='install_id'"); err != nil {
		return fmt.Errorf("locking bootstrap: %w", err)
	}
	queries := s.store.Queries.WithTx(tx)
	count, err := queries.CountKeys(ctx)
	if err != nil {
		return fmt.Errorf("checking bootstrap: %w", err)
	}
	input := databasePopulated
	if count == 0 {
		input = databaseEmpty
	}
	state, commands := step(bootstrapStart, input)
	var params db.CreateKeyParams
	var raw string
	filename := filepath.Join(dataDir, "admin.key")
	for len(commands) > 0 {
		command := commands[0]
		var next bootstrapInput
		switch command {
		case writeAdminFile:
			params, raw, err = makeKey("bootstrap admin", true, s.clock.Now())
			if err == nil {
				err = writeAdmin(ctx, filename, raw)
			}
			next = fileWritten
		case commitAdminKey:
			_, err = queries.CreateKey(ctx, params)
			if err == nil {
				err = tx.Commit()
			}
			next = keyCommitted
		case printAdminKey:
			_, err = fmt.Fprintln(out, raw)
			next = keyPrinted
		case removeAdminFile:
			cleanupErr := os.Remove(filename)
			if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("removing uncommitted admin file: %w", cleanupErr))
			}
			return fmt.Errorf("bootstrapping admin key: %w", err)
		}
		if err != nil {
			state, commands = step(state, operationFailed)
			if len(commands) == 0 {
				return fmt.Errorf("bootstrapping admin key: %w", err)
			}
			continue
		}
		state, commands = step(state, next)
	}
	return nil
}

func writeAdmin(ctx context.Context, filename, raw string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-controlled data directory
	if err != nil {
		return fmt.Errorf("creating admin key file: %w", err)
	}
	_, writeErr := io.WriteString(file, raw+"\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		removeErr := os.Remove(filename)
		return fmt.Errorf("writing admin key file: %w", errors.Join(err, removeErr))
	}
	return nil
}
