// Command controller is the argorun-controller entrypoint: leader election
// gates the auto-reconcile loop (only the leader deploys), while every
// replica serves the manual-sync API (§5.3/§5.4/§5.9).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/argorun/argorun/internal/api"
	"github.com/argorun/argorun/internal/auth"
	"github.com/argorun/argorun/internal/cloudrun"
	"github.com/argorun/argorun/internal/config"
	"github.com/argorun/argorun/internal/expander"
	"github.com/argorun/argorun/internal/githubapp"
	"github.com/argorun/argorun/internal/gitsource"
	"github.com/argorun/argorun/internal/leader"
	"github.com/argorun/argorun/internal/notify"
	"github.com/argorun/argorun/internal/precondition"
	"github.com/argorun/argorun/internal/rbac"
	"github.com/argorun/argorun/internal/reconcile"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// requiredEnv returns os.Getenv(key), or an error if it's unset — every
// trust-boundary/deploy-target input fails startup loudly rather than
// silently defaulting (§7).
func requiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// run does the actual work so its defers (closing db/clients, stopping
// signal notification, shutting down the HTTP server) always execute before
// main reports an error and exits.
func run() error {
	dsn, err := requiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	// IAP_AUDIENCE is a trust-boundary input (the expected aud claim on
	// Identity-Aware Proxy's signed identity assertion — see
	// https://cloud.google.com/iap/docs/signed-headers-howto) — no default.
	// The service is expected to be fronted by IAP, which has already
	// authenticated the caller; this is defense-in-depth verification of
	// that assertion, not the primary access gate (IAM is).
	iapAudience, err := requiredEnv("IAP_AUDIENCE")
	if err != nil {
		return err
	}
	configRepo, err := requiredEnv("ARGORUN_CONFIG_REPO")
	if err != nil {
		return err
	}
	configBranch, err := requiredEnv("ARGORUN_CONFIG_BRANCH")
	if err != nil {
		return err
	}
	configPath, err := requiredEnv("ARGORUN_CONFIG_PATH")
	if err != nil {
		return err
	}
	githubAppID, err := requiredEnv("GITHUB_APP_ID")
	if err != nil {
		return err
	}
	githubAppPEM, err := requiredEnv("GITHUB_APP_PEM")
	if err != nil {
		return err
	}
	rbacPath := envOrDefault("RBAC_PATH", "rbac.yaml")
	httpAddr := envOrDefault("HTTP_ADDR", ":8080")
	reconcileInterval, err := time.ParseDuration(envOrDefault("RECONCILE_INTERVAL", "30s"))
	if err != nil {
		return fmt.Errorf("RECONCILE_INTERVAL: %w", err)
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

	ghClient, err := githubapp.NewClient(githubAppID, []byte(githubAppPEM))
	if err != nil {
		return fmt.Errorf("build GitHub App client: %w", err)
	}

	iapAuth, err := auth.NewIAPAuthenticator(iapAudience)
	if err != nil {
		return fmt.Errorf("build IAP authenticator: %w", err)
	}

	cfgSrc := configSource{repo: configRepo, branch: configBranch, path: configPath, rbacPath: rbacPath}
	root, units, err := loadUnits(ctx, ghClient, cfgSrc)
	if err != nil {
		return fmt.Errorf("initial config load: %w", err)
	}
	rbacCfg, err := loadRBAC(ctx, ghClient, cfgSrc)
	if err != nil {
		log.Printf("startup: %v — manual sync will be denied until %s exists", err, rbacPath)
		rbacCfg = &rbac.Config{}
	}

	cloudRun := cloudrun.NewGCPAdminClient()
	defer func() { _ = cloudRun.Close() }()
	preconditions := precondition.NewGCPChecker()
	defer func() { _ = preconditions.Close() }()

	var notifier reconcile.Notifier
	if root.Notify.SlackWebhookURL != "" {
		notifier = &notify.Evaluator{
			DB:    db,
			Sink:  &notify.SlackSink{WebhookURL: root.Notify.SlackWebhookURL},
			Rules: root.Notify.Rules,
		}
	}

	reconciler := &reconcile.Reconciler{
		DB:            db,
		CloudRun:      cloudRun,
		Preconditions: preconditions,
		Manifests:     &gitsource.Source{Client: ghClient},
		ManagedFields: root.Defaults.ManagedFields,
		Workers:       reconcile.DefaultWorkers,
		Notifier:      notifier,
	}

	dynUnits := &dynamicUnits{}
	dynUnits.set(units)

	handler := &api.Handler{
		Auth:       iapAuth,
		RBAC:       rbacCfg,
		Units:      dynUnits,
		Status:     &api.PostgresStatusStore{DB: db},
		Reconciler: reconciler,
	}
	srv := &http.Server{Addr: httpAddr, Handler: api.NewMux(handler), ReadHeaderTimeout: 10 * time.Second}

	lc := newLeadershipContext(ctx)
	lease := leader.New(db, holderID)

	var wg sync.WaitGroup
	wg.Add(3)

	// Buffered so the goroutine can send and exit even if nothing is
	// receiving yet (e.g. the process is already tearing down for another
	// reason).
	serverErrCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- err
		}
	}()

	go func() {
		defer wg.Done()
		runLeaderElection(ctx, lease, lc, holderID)
	}()

	go func() {
		defer wg.Done()
		reconcileLoop(ctx, reconcileInterval, lc, ghClient, cfgSrc, reconciler, dynUnits)
	}()

	// A bind/serve failure (e.g. the port is already taken) is fatal, not a
	// log line to ignore: it means the manual-sync API never came up at
	// all, with no signal for the surrounding orchestrator (Cloud Run,
	// k8s) that anything's wrong. Cancel everything else and report it.
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrCh:
		runErr = fmt.Errorf("http server: %w", err)
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	wg.Wait()
	return runErr
}

// runLeaderElection restarts lease.Run after any error instead of letting
// the goroutine die silently — a transient DB blip shouldn't permanently
// strand this replica out of leader election until a manual restart.
// lease.Run itself already reports leading(false) before returning an
// error (internal/leader/lease.go), so lc is already correctly reset by
// the time this loop retries.
func runLeaderElection(ctx context.Context, lease *leader.Lease, lc *leadershipContext, holderID string) {
	const maxBackoff = 30 * time.Second
	backoff := time.Second
	for {
		err := lease.Run(ctx, func(leading bool) {
			lc.set(ctx, leading)
			// holderID is HOSTNAME/os.Hostname(), operator-controlled, not
			// external input.
			log.Printf("leadership changed: leading=%v holder=%s", leading, holderID) //nolint:gosec
		})
		if ctx.Err() != nil {
			return
		}
		log.Printf("leader election stopped, retrying in %s: %v", backoff, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// leadershipContext holds a context valid only for the current leadership
// term, cancelled the instant leadership changes. reconcileLoop runs
// RunOnce with the context returned by Current() at the start of a pass —
// if leadership is lost mid-pass, that context is cancelled out from under
// it, so in-flight Cloud Run/DB calls for that pass abort instead of
// continuing to deploy after the lease is gone. A plain isLeader.Load()
// boolean checked once before RunOnce starts can't do this: leadership
// could be lost seconds into a multi-unit pass with nothing to stop it.
type leadershipContext struct {
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// newLeadershipContext starts in the "not leader" state (an
// already-cancelled context), since this replica hasn't claimed the lease
// yet.
func newLeadershipContext(parent context.Context) *leadershipContext {
	ctx, cancel := context.WithCancel(parent)
	cancel()
	return &leadershipContext{ctx: ctx, cancel: cancel}
}

func (l *leadershipContext) set(parent context.Context, leading bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cancel() // end whatever term was active; a no-op if already cancelled
	if leading {
		l.ctx, l.cancel = context.WithCancel(parent)
		return
	}
	ctx, cancel := context.WithCancel(parent)
	cancel()
	l.ctx, l.cancel = ctx, cancel
}

// Current returns the context for whatever leadership term is active right
// now — cancelled already if this replica isn't leader.
func (l *leadershipContext) Current() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ctx
}

type configSource struct {
	repo, branch, path, rbacPath string
}

func loadUnits(ctx context.Context, gh *githubapp.Client, cs configSource) (*config.Root, []expander.SyncUnit, error) {
	data, err := gh.GetFile(ctx, cs.repo, cs.branch, cs.path)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", cs.path, err)
	}
	root, err := config.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	units, err := expander.Expand(root)
	if err != nil {
		return nil, nil, err
	}
	return root, units, nil
}

func loadRBAC(ctx context.Context, gh *githubapp.Client, cs configSource) (*rbac.Config, error) {
	data, err := gh.GetFile(ctx, cs.repo, cs.branch, cs.rbacPath)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", cs.rbacPath, err)
	}
	return rbac.Parse(data)
}

// dynamicUnits is a UnitLookup refreshed every reconcile pass, so a newly
// added app is reachable by the manual-sync API without a controller
// restart. RBAC/notify config and managedFields are only read at startup —
// ponytail: restart to pick those up, add a full config-hot-reload path if
// that friction matters later.
type dynamicUnits struct {
	mu    sync.RWMutex
	units map[string]expander.SyncUnit
}

func (d *dynamicUnits) set(units []expander.SyncUnit) {
	m := make(map[string]expander.SyncUnit, len(units))
	for _, u := range units {
		m[u.App+"/"+u.Project] = u
	}
	d.mu.Lock()
	d.units = m
	d.mu.Unlock()
}

func (d *dynamicUnits) Find(app, project string) (expander.SyncUnit, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	u, ok := d.units[app+"/"+project]
	return u, ok
}

// List implements api.UnitLister.
func (d *dynamicUnits) List() []expander.SyncUnit {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]expander.SyncUnit, 0, len(d.units))
	for _, u := range d.units {
		out = append(out, u)
	}
	return out
}

func reconcileLoop(ctx context.Context, interval time.Duration, lc *leadershipContext, gh *githubapp.Client, cs configSource, reconciler *reconcile.Reconciler, units *dynamicUnits) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, expanded, err := loadUnits(ctx, gh, cs)
			if err != nil {
				log.Printf("reconcile: reload config: %v", err)
				continue
			}
			units.set(expanded)

			// passCtx is this leadership term's context, not the raw ctx —
			// if leadership is lost partway through RunOnce, passCtx is
			// cancelled and in-flight work for this pass aborts.
			passCtx := lc.Current()
			if passCtx.Err() != nil {
				continue // not leader
			}
			if _, err := reconciler.RunOnce(passCtx, expanded); err != nil {
				log.Printf("reconcile: run once: %v", err)
			}
		}
	}
}
