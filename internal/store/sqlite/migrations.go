package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrateUp(ctx context.Context, db *sql.DB, dbFilePath string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	source, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("opening embedded migrations: %w", err)
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing embedded migrations: %w", closeErr))
		}
	}()

	database, err := migratesqlite.WithInstance(db, &migratesqlite.Config{
		DatabaseName: dbFilePath,
	})
	if err != nil {
		return fmt.Errorf("opening migration database: %w", err)
	}
	// The migration database driver borrows db; closing it would close the store connection.

	migrator, err := migrate.NewWithInstance("iofs", source, "sqlite", database)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}

	stopCancellation := context.AfterFunc(ctx, func() {
		migrator.GracefulStop <- true
	})
	defer stopCancellation()

	if err := migrator.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying embedded migrations: %w", err)
	}
	return ctx.Err()
}
