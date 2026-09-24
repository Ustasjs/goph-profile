// Package migrations applies the embedded SQL migrations.
// The binary carries them, so it does not depend on the working
// directory.
package migrations

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// The migrate driver works over database/sql, so the pgx
	// stdlib shim is needed here even though the app itself uses
	// pgxpool.
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed *.sql
var fs embed.FS

// Run opens a short connection to dsn and applies all pending
// migrations. A database that is already up to date is not an
// error.
func Run(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer func() { _ = db.Close() }()

	source, err := iofs.New(fs, ".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("create migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
