// Command runcd is a CLI client for the runcd controller's HTTP API
// (internal/api) — list sync units, inspect a diff, view sync history,
// check who can sync what, and trigger a manual sync, all from a
// terminal instead of the dashboard.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// readTimeout bounds the fast, read-only endpoints (units/get/history/
// rbac). syncTimeout is far more generous: handleSync blocks synchronously
// through fetch->diff->precondition->deploy->re-fetch, which routinely
// exceeds readTimeout for a real Cloud Run deploy — a sync that times out
// client-side still left the deploy running server-side, so a low timeout
// here just trades a clear in-progress wait for a confusing client error
// and a subsequent 409 on retry.
const (
	readTimeout = 30 * time.Second
	syncTimeout = 10 * time.Minute
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = `runcd — CLI client for the runcd API

Usage:
  runcd units                          list every configured sync unit
  runcd get <project> <app>            show one unit's desired-vs-live diff
  runcd history <project> <app>        show sync history for one unit
  runcd sync <project> <app>           trigger a manual sync
  runcd sync <project> <app> --dry-run preview a sync without deploying anything
  runcd rbac                           list configured RBAC roles
  runcd orphans                        list live Cloud Run services absent from config

Configuration (env vars):
  RUNCD_API_URL      required — base URL to call. If the dashboard is
                     deployed as its own IAP-fronted Cloud Run service (the
                     documented setup — see README), point this at its
                     proxy, e.g. https://<dashboard>.run.app/api/proxy. The
                     controller service itself typically has no IAP, only
                     Cloud Run IAM invoker scoped to the dashboard's own
                     service account — a human generally can't call it
                     directly without also being granted that role.
  RUNCD_IAP_AUDIENCE optional — if RUNCD_API_URL is behind IAP, the IAP
                     resource audience to request a token for (the same
                     value the controller's own IAP_AUDIENCE env var
                     uses). runcd shells out to
                     'gcloud auth print-identity-token --audiences=...'
                     for a token, reusing whatever account is already
                     active in gcloud. Omit if there's no IAP in front.
`

// run does the actual work, taking args/stdout/stderr as parameters so
// it's testable without exec-ing the real binary.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("no command given")
	}

	cmd, rest := args[0], args[1:]

	// help/-h/--help must work with no configuration at all — a brand-new
	// user with RUNCD_API_URL unset yet still needs to see the command
	// list (this is the README's own suggested first step), not a config
	// error before the switch below even looks at cmd.
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	}

	baseURL := os.Getenv("RUNCD_API_URL")
	if baseURL == "" {
		return errors.New("RUNCD_API_URL is required")
	}

	background := context.Background()
	token, err := identityToken(background, os.Getenv("RUNCD_IAP_AUDIENCE"))
	if err != nil {
		return err
	}
	c := newClient(baseURL, token)

	switch cmd {
	case "units":
		ctx, cancel := context.WithTimeout(background, readTimeout)
		defer cancel()
		units, err := c.listUnits(ctx)
		if err != nil {
			return err
		}
		renderUnits(stdout, units)
		return nil

	case "get":
		project, app, err := requireTwoArgs(rest, "get <project> <app>")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(background, readTimeout)
		defer cancel()
		u, err := c.getUnit(ctx, project, app)
		if err != nil {
			return err
		}
		renderUnit(stdout, u)
		return nil

	case "history":
		project, app, err := requireTwoArgs(rest, "history <project> <app>")
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(background, readTimeout)
		defer cancel()
		events, err := c.getHistory(ctx, project, app)
		if err != nil {
			return err
		}
		renderHistory(stdout, events)
		return nil

	case "sync":
		positional, dryRun := extractFlag(rest, "--dry-run")
		project, app, err := requireTwoArgs(positional, "sync <project> <app> [--dry-run]")
		if err != nil {
			return err
		}
		if dryRun {
			// dry-run makes the same real Cloud Run/Pub-Sub calls a sync
			// does (just without deploying), so it needs the same generous
			// timeout as orphans/sync, not the fast read-only budget.
			ctx, cancel := context.WithTimeout(background, syncTimeout)
			defer cancel()
			res, err := c.dryRun(ctx, project, app)
			if err != nil {
				return err
			}
			renderDryRun(stdout, res)
			return nil
		}
		ctx, cancel := context.WithTimeout(background, syncTimeout)
		defer cancel()
		res, err := c.sync(ctx, project, app)
		if err != nil {
			return err
		}
		renderSyncResponse(stdout, res)
		return nil

	case "rbac":
		ctx, cancel := context.WithTimeout(background, readTimeout)
		defer cancel()
		rules, err := c.listRBAC(ctx)
		if err != nil {
			return err
		}
		renderRBAC(stdout, rules)
		return nil

	case "orphans":
		// syncTimeout, not readTimeout: unlike every other read endpoint
		// (Postgres-only), orphans fans out live Cloud Run ListServices
		// calls across every project/region in the config — on a
		// real-sized fleet that can legitimately take longer than 30s.
		ctx, cancel := context.WithTimeout(background, syncTimeout)
		defer cancel()
		orphans, err := c.listOrphans(ctx)
		if err != nil {
			return err
		}
		renderOrphans(stdout, orphans)
		return nil

	default:
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func requireTwoArgs(args []string, usage string) (string, string, error) {
	if len(args) != 2 {
		return "", "", fmt.Errorf("usage: runcd %s", usage)
	}
	return args[0], args[1], nil
}

// extractFlag reports whether flag is present anywhere in args and returns
// the remaining positional args with it removed — good enough for a single
// boolean flag alongside two fixed positional args, not a general flag
// parser.
func extractFlag(args []string, flag string) (remaining []string, present bool) {
	for _, a := range args {
		if a == flag {
			present = true
			continue
		}
		remaining = append(remaining, a)
	}
	return remaining, present
}
