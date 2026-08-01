// Command controller is the runcd-controller entrypoint: leader election
// gates the auto-reconcile loop (only the leader deploys), while every
// replica serves the manual-sync API (§5.3/§5.4/§5.9).
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/runcd/runcd/internal/api"
	"github.com/runcd/runcd/internal/auth"
	"github.com/runcd/runcd/internal/cloudrun"
	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/folders"
	"github.com/runcd/runcd/internal/githubapp"
	"github.com/runcd/runcd/internal/gitsource"
	"github.com/runcd/runcd/internal/leader"
	"github.com/runcd/runcd/internal/notify"
	"github.com/runcd/runcd/internal/precondition"
	"github.com/runcd/runcd/internal/rbac"
	"github.com/runcd/runcd/internal/reconcile"
	"github.com/runcd/runcd/internal/store"
)

// shutdownWaitTimeout bounds how long shutdown waits for the server/leader-
// election/reconcile-loop goroutines to exit after ctx is cancelled, before
// giving up and returning anyway — see its use in run().
const shutdownWaitTimeout = 15 * time.Second

func main() {
	// JSON, not text: Cloud Logging ingests stdout/stderr as jsonPayload
	// when it's valid JSON, giving structured fields and correct severity
	// levels instead of an opaque textPayload line.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("controller exited", "error", err)
		os.Exit(1)
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

// randomID returns an 8-byte random hex string, generated once at process
// startup for holderID (see its own comment) — crypto/rand, not math/rand,
// since two instances racing to start at the exact same instant must not
// be able to collide on a predictable seed.
func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Astronomically unlikely (crypto/rand reading from the OS CSPRNG),
		// but holderID must never end up empty — fall back to a
		// timestamp-derived value rather than leaving two instances both
		// computing the same empty string.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// openDB opens the controller's database connection. Two modes, chosen by
// whether CLOUDSQL_INSTANCE_CONNECTION_NAME is set:
//
//   - Set: dial the named Cloud SQL instance via the Cloud SQL Go Connector
//     with IAM database authentication — no password, no DATABASE_URL. The
//     connecting user (CLOUDSQL_IAM_DB_USER) must be a Cloud SQL user
//     mapped to an IAM principal (a human user or service account already
//     granted access via IAM), and IAM auth must be enabled on the
//     instance. This is the preferred path: no DB password to manage or
//     leak, consistent with everything else here being IAM/IAP-gated.
//   - Unset: DATABASE_URL, a plain connection string — unchanged fallback
//     for anyone not using Cloud SQL IAM auth.
//
// The returned close func closes both db itself and (in Cloud SQL IAM mode)
// the dialer's background token-refresh goroutines — one call for the
// caller to defer, not two to remember separately.
func openDB(ctx context.Context) (*sql.DB, func() error, error) {
	instanceConnName := os.Getenv("CLOUDSQL_INSTANCE_CONNECTION_NAME")
	if instanceConnName == "" {
		dsn, err := requiredEnv("DATABASE_URL")
		if err != nil {
			return nil, nil, err
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("open database: %w", err)
		}
		return db, db.Close, nil
	}

	dbUser, err := requiredEnv("CLOUDSQL_IAM_DB_USER")
	if err != nil {
		return nil, nil, err
	}
	dbName, err := requiredEnv("CLOUDSQL_DB_NAME")
	if err != nil {
		return nil, nil, err
	}

	dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN())
	if err != nil {
		return nil, nil, fmt.Errorf("create Cloud SQL dialer: %w", err)
	}

	// sslmode=disable: the connector itself establishes an authenticated,
	// encrypted connection via DialFunc below — there's no plaintext TCP
	// hop for pgx's own TLS negotiation to secure.
	connConfig, err := pgx.ParseConfig(fmt.Sprintf("user=%s dbname=%s sslmode=disable", dbUser, dbName))
	if err != nil {
		_ = dialer.Close()
		return nil, nil, fmt.Errorf("parse Cloud SQL connection config: %w", err)
	}
	connConfig.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.Dial(ctx, instanceConnName)
	}

	db := stdlib.OpenDB(*connConfig)
	closeAll := func() error {
		return errors.Join(db.Close(), dialer.Close())
	}
	return db, closeAll, nil
}

// run does the actual work so its defers (closing db/clients, stopping
// signal notification, shutting down the HTTP server) always execute before
// main reports an error and exits.
func run() error {
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
	configRepo, err := requiredEnv("RUNCD_CONFIG_REPO")
	if err != nil {
		return err
	}
	configBranch, err := requiredEnv("RUNCD_CONFIG_BRANCH")
	if err != nil {
		return err
	}
	configPath, err := requiredEnv("RUNCD_CONFIG_PATH")
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

	// Neither $HOSTNAME nor os.Hostname() reliably return anything
	// per-instance-unique on Cloud Run (unlike Kubernetes, where HOSTNAME is
	// the pod name) — observed in practice resolving to the literal string
	// "localhost" for every concurrently-running instance once the service
	// autoscaled past one replica, which made two genuinely different
	// containers indistinguishable to the leader_lease claim logic: each
	// kept "stealing" the lease from what looked like itself, flapping
	// leadership every few seconds and cancelling every in-flight
	// reconcile pass mid-tick. holderID only needs to be unique among
	// currently-running processes, not stable across restarts, so a random
	// suffix generated once at boot is sufficient — prefixed with
	// K_REVISION (Cloud Run's own env var, same across every replica of one
	// revision) purely so the sync_locks/leader_lease holder column stays
	// readable in logs/DB inspection.
	holderID := envOrDefault("K_REVISION", "controller") + "-" + randomID()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, closeDB, err := openDB(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = closeDB() }()

	// Unbounded (database/sql's own default) risks connection exhaustion
	// under a concurrent spike — this one pool is shared by reconcile
	// workers (up to reconcile.DefaultWorkers concurrent deploys),
	// dashboard-read API requests, leader-election renewal, and notify's
	// debounce transaction, all potentially overlapping. Generous enough
	// over the worker pool's own default size not to bottleneck normal
	// operation, configurable for a deployment that needs otherwise.
	maxOpenConns, err := strconv.Atoi(envOrDefault("DB_MAX_OPEN_CONNS", "25"))
	if err != nil {
		return fmt.Errorf("DB_MAX_OPEN_CONNS: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Idempotent — safe on every boot, including one replica racing another
	// to apply it for the first time (see store.Apply's doc comment).
	if err := store.Apply(ctx, db); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	ghClient, err := githubapp.NewClient(githubAppID, []byte(githubAppPEM))
	if err != nil {
		return fmt.Errorf("build GitHub App client: %w", err)
	}

	iapAuth, err := auth.NewIAPAuthenticator(iapAudience)
	if err != nil {
		return fmt.Errorf("build IAP authenticator: %w", err)
	}

	folderResolver, err := folders.NewGCPResolver(ctx)
	if err != nil {
		return fmt.Errorf("build resource manager client: %w", err)
	}
	defer func() { _ = folderResolver.Close() }()

	cfgSrc := configSource{repo: configRepo, branch: configBranch, path: configPath, rbacPath: rbacPath}
	root, units, err := loadUnits(ctx, ghClient, cfgSrc, folderResolver)
	if err != nil {
		return fmt.Errorf("initial config load: %w", err)
	}
	rbacCfg, err := loadRBAC(ctx, ghClient, cfgSrc)
	if err != nil {
		slog.Error("startup: manual sync will be denied until rbac.yaml exists", "error", err, "rbacPath", rbacPath)
		rbacCfg = &rbac.Config{}
	}
	rbacStore := rbac.NewStore(rbacCfg)
	if membership, err := loadRBACFolderMembership(ctx, folderResolver, rbacCfg); err != nil {
		slog.Error("startup: resolve rbac folder scopes", "error", err)
	} else {
		rbacStore.SetFolderMembership(membership)
	}

	cloudRun := cloudrun.NewGCPAdminClient()
	defer func() { _ = cloudRun.Close() }()
	preconditions := precondition.NewGCPChecker()
	defer func() { _ = preconditions.Close() }()

	reconcilerPtr := &atomic.Pointer[reconcile.Reconciler]{}
	reconcilerPtr.Store(&reconcile.Reconciler{
		DB:            db,
		CloudRun:      cloudRun,
		Preconditions: preconditions,
		Manifests:     &gitsource.Source{Client: ghClient},
		ManagedFields: root.Defaults.ManagedFields,
		Workers:       reconcile.DefaultWorkers,
		Notifier:      buildNotifier(db, root),
	})

	dynUnits := &dynamicUnits{}
	dynUnits.set(units)

	statusStore := &api.PostgresStatusStore{DB: db}
	metricsHandler, err := api.NewMetricsHandler(statusStore)
	if err != nil {
		return fmt.Errorf("build metrics handler: %w", err)
	}

	handler := &api.Handler{
		Auth:       iapAuth,
		RBAC:       rbacStore,
		Units:      dynUnits,
		Status:     statusStore,
		Metrics:    metricsHandler,
		Reconciler: reconcilerPtr,
		RuntimeInfo: api.RuntimeInfo{
			ConfigRepo:               configRepo,
			ConfigBranch:             configBranch,
			ConfigPath:               configPath,
			RBACPath:                 rbacPath,
			ReconcileIntervalSeconds: int(reconcileInterval.Seconds()),
		},
	}
	srv := &http.Server{Addr: httpAddr, Handler: api.NewMux(handler), ReadHeaderTimeout: 10 * time.Second}

	// A separate, dedicated *sql.DB (own tiny pool) — not db, the pool
	// shared by reconcile workers, dashboard-read API requests, and
	// notify's debounce transaction. Leader election is small and
	// latency-sensitive (one UPDATE, must complete every RenewInterval);
	// sharing a pool with much heavier application traffic means a busy
	// tick (more units, a slow notify send, ...) can starve Claim() of a
	// connection until it times out — observed in practice as a real
	// production incident: leadership flapped every few seconds under
	// load, each flap cancelling every in-flight reconcile pass for every
	// unit via leadershipContext, which only made the load (and thus the
	// contention) worse. A structurally separate pool means reconcile load
	// can never starve leader election, no matter how busy it gets.
	leaderDB, closeLeaderDB, err := openDB(ctx)
	if err != nil {
		return fmt.Errorf("open leader-election database connection: %w", err)
	}
	defer func() { _ = closeLeaderDB() }()
	leaderDB.SetMaxOpenConns(2)
	leaderDB.SetMaxIdleConns(2)
	leaderDB.SetConnMaxLifetime(30 * time.Minute)

	lc := newLeadershipContext(ctx)
	lease := leader.New(leaderDB, holderID)

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
		reconcileLoop(ctx, reconcileInterval, lc, ghClient, cfgSrc, db, reconcilerPtr, dynUnits, rbacStore, folderResolver)
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

	// wg.Wait() itself has no deadline — every goroutine it covers is
	// ctx-cancellation-aware, but a bug or a wedged call ignoring ctx
	// shouldn't hang process exit forever. Best-effort: log and return
	// anyway rather than block indefinitely.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWaitTimeout):
		slog.Error("shutdown: goroutines didn't exit within timeout, exiting anyway")
	}
	return runErr
}

// runLeaderElection restarts lease.Run after any error instead of letting
// the goroutine die silently — a transient DB blip shouldn't permanently
// strand this replica out of leader election until a manual restart.
// lease.Run itself already reports leading(false) before returning an
// error (internal/leader/lease.go), so lc is already correctly reset by
// the time this loop retries.
func runLeaderElection(ctx context.Context, lease *leader.Lease, lc *leadershipContext, holderID string) {
	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
		// resetBackoffAfter: a run that lasted at least this long is
		// evidence the connection was healthy, not just barely surviving —
		// without this, backoff only ever grows for the life of the
		// process, so a couple of unrelated blips separated by hours of
		// stable leadership would still pin every later blip's retry at
		// the full 30s cap.
		resetBackoffAfter = 2 * time.Minute
	)
	backoff := initialBackoff
	for {
		start := time.Now()
		err := lease.Run(ctx, func(leading bool) {
			lc.set(ctx, leading)
			slog.Info("leadership changed", "leading", leading, "holder", holderID)
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= resetBackoffAfter {
			backoff = initialBackoff
		}
		slog.Error("leader election stopped, retrying", "backoff", backoff, "error", err)
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

func loadUnits(ctx context.Context, gh *githubapp.Client, cs configSource, resolver folders.Resolver) (*config.Root, []expander.SyncUnit, error) {
	data, err := gh.GetFile(ctx, cs.repo, cs.branch, cs.path)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", cs.path, err)
	}
	root, err := config.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	// A folder-resolution error here is logged, not propagated as fatal:
	// ResolveConfig already falls back to each affected environment's
	// explicitly-listed Projects on a per-folder failure, and the whole
	// tick — every other environment's config/RBAC/notify reload and
	// RunOnce for every unit — shouldn't abort over one folder's
	// transient Resource Manager error. root is always usable even when
	// resolveErr is non-nil (see ResolveConfig's own doc comment).
	root, resolveErr := folders.ResolveConfig(ctx, resolver, root)
	if resolveErr != nil {
		slog.Error("reconcile: resolve environments[].folders", "error", resolveErr)
	}
	if root == nil {
		// Never actually happens — ResolveConfig always returns a usable
		// *config.Root, even alongside a non-nil error (see its own doc
		// comment) — but nilaway can't prove that across the call, and
		// flags every downstream access of root.Defaults otherwise. A real
		// guard costs nothing and satisfies it without excluding this file
		// from nilaway checking altogether.
		return nil, nil, fmt.Errorf("resolve environments[].folders: %w", resolveErr)
	}
	units, err := expander.Expand(root)
	if err != nil {
		return nil, nil, err
	}
	return root, units, nil
}

// loadRBACFolderMembership resolves every folder ID rbacCfg's rules
// reference via "folder:<id>" scopes into its member projects — a separate
// resolution pass from loadUnits' own environments[].folders (a distinct
// set of folder IDs, potentially overlapping but not assumed to), stored
// via rbacStore.SetFolderMembership.
func loadRBACFolderMembership(ctx context.Context, resolver folders.Resolver, rbacCfg *rbac.Config) (map[string][]string, error) {
	ids := rbac.FolderScopes(rbacCfg)
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	return folders.ResolveMembership(ctx, resolver, ids)
}

func loadRBAC(ctx context.Context, gh *githubapp.Client, cs configSource) (*rbac.Config, error) {
	data, err := gh.GetFile(ctx, cs.repo, cs.branch, cs.rbacPath)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", cs.rbacPath, err)
	}
	return rbac.Parse(data)
}

// buildNotifier returns nil if no Slack webhook is configured — the
// interface-typed nil that reconcile.Reconciler's r.Notifier == nil check
// needs. Assigning a typed *notify.Evaluator to a reconcile.Notifier
// variable unconditionally would make that check false forever (a non-nil
// interface holding a nil-webhook Evaluator), so both the startup and
// hot-reload call sites go through this one function rather than
// duplicating the conditional.
func buildNotifier(db *sql.DB, root *config.Root) reconcile.Notifier {
	if root.Notify.SlackWebhookURL == "" {
		return nil
	}
	return &notify.Evaluator{
		DB:    db,
		Sink:  &notify.SlackSink{WebhookURL: root.Notify.SlackWebhookURL},
		Rules: root.Notify.Rules,
	}
}

// dynamicUnits is a UnitLookup refreshed every reconcile pass, so a newly
// added app is reachable by the manual-sync API without a controller
// restart. RBAC, notify config, and managedFields are all refreshed on the
// same cadence — see reconcileLoop.
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

func reconcileLoop(ctx context.Context, interval time.Duration, lc *leadershipContext, gh *githubapp.Client, cs configSource, db *sql.DB, reconcilerPtr *atomic.Pointer[reconcile.Reconciler], units *dynamicUnits, rbacStore *rbac.Store, folderResolver folders.Resolver) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// lastNotify/lastManagedFields track what the currently-stored
	// Reconciler was built with, so a reload that hasn't actually changed
	// either one (the common case — most ticks) doesn't need to construct
	// and swap in a new Reconciler at all. Zero-valued on the first tick,
	// which just means that tick's rebuild is a harmless no-op if root
	// hasn't diverged from startup yet.
	var lastNotify config.Notify
	var lastManagedFields []string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// config, RBAC, and notify reload independently (§8) — a
			// transient config-fetch error must not also skip the RBAC/
			// folder-membership reload below just because they happen to
			// be fetched in the same tick; only the config-dependent steps
			// (unit list, RunOnce) are skipped this tick.
			root, expanded, configErr := loadUnits(ctx, gh, cs, folderResolver)
			if configErr != nil {
				slog.Error("reconcile: reload config", "error", configErr)
			} else {
				units.set(expanded)

				if !reflect.DeepEqual(root.Notify, lastNotify) || !reflect.DeepEqual(root.Defaults.ManagedFields, lastManagedFields) {
					next := *reconcilerPtr.Load() // shallow copy: DB/CloudRun/Preconditions/Manifests/Workers carry over unchanged
					next.ManagedFields = root.Defaults.ManagedFields
					next.Notifier = buildNotifier(db, root)
					reconcilerPtr.Store(&next)
					lastNotify, lastManagedFields = root.Notify, root.Defaults.ManagedFields
				}
			}

			// A missing/invalid rbac.yaml here doesn't fail closed to empty
			// like the startup path does — that would silently revoke every
			// existing grant on a transient fetch error. Keep serving the
			// last-known-good rules and just log it.
			if newRBAC, err := loadRBAC(ctx, gh, cs); err != nil {
				slog.Error("reconcile: reload rbac", "rbacPath", cs.rbacPath, "error", err)
			} else {
				rbacStore.Set(newRBAC)
				if membership, err := loadRBACFolderMembership(ctx, folderResolver, newRBAC); err != nil {
					// Keep serving the last-known-good membership map, same
					// posture as a failed rbac.yaml reload above — a
					// transient Resource Manager error shouldn't revoke
					// every folder-scoped grant.
					slog.Error("reconcile: resolve rbac folder scopes", "error", err)
				} else {
					rbacStore.SetFolderMembership(membership)
				}
			}

			if configErr != nil {
				continue // no fresh unit list to run this tick's reconcile pass over
			}

			// passCtx is this leadership term's context, not the raw ctx —
			// if leadership is lost partway through RunOnce, passCtx is
			// cancelled and in-flight work for this pass aborts.
			passCtx := lc.Current()
			if passCtx.Err() != nil {
				continue // not leader
			}
			results, err := reconcilerPtr.Load().RunOnce(passCtx, expanded)
			if err != nil {
				slog.Error("reconcile: run once", "error", err)
			}
			// RunOnce's own error is just the first of possibly several
			// concurrent failures with no unit attribution (see its comment) —
			// without this, a unit landing on Invalid/Missing is otherwise
			// silent: the reason lives only in the applications table's
			// status column, never in the logs.
			for _, res := range results {
				if res.Err != nil {
					slog.Error("reconcile", "app", res.Unit.App, "project", res.Unit.Project, "error", res.Err)
				}
			}
		}
	}
}
