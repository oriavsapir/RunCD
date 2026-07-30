// Command controller is the argorun-controller entrypoint. Phase 1 wires up
// leader election only — the reconcile loop (internal/reconcile) isn't
// driven from here yet since it still needs a real git-polling
// ManifestSource and Cloud Run Admin API client (§5.4/§5.5).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/argorun/argorun/internal/leader"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run does the actual work so its defers (closing db, stopping signal
// notification) always execute before main reports an error and exits.
func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	holderID := os.Getenv("HOSTNAME")
	if holderID == "" {
		holderID, _ = os.Hostname()
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	l := leader.New(db, holderID)
	err = l.Run(ctx, func(leading bool) {
		// holderID is HOSTNAME/os.Hostname(), operator-controlled, not
		// external input.
		log.Printf("leadership changed: leading=%v holder=%s", leading, holderID) //nolint:gosec
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("leader election stopped: %w", err)
	}
	return nil
}
