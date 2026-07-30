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
	"sync/atomic"
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
	// AUTH_AUDIENCE is a trust-boundary input (the OAuth client ID every
	// manual-sync request's ID token must be issued for) — no default.
	audience, err := requiredEnv("AUTH_AUDIENCE")
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
		Auth:       &auth.GoogleAuthenticator{Audience: audience},
		RBAC:       rbacCfg,
		Units:      dynUnits,
		Reconciler: reconciler,
	}
	srv := &http.Server{Addr: httpAddr, Handler: api.NewMux(handler), ReadHeaderTimeout: 10 * time.Second}

	var isLeader atomic.Bool
	lease := leader.New(db, holderID)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server error: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		err := lease.Run(ctx, func(leading bool) {
			isLeader.Store(leading)
			// holderID is HOSTNAME/os.Hostname(), operator-controlled, not
			// external input.
			log.Printf("leadership changed: leading=%v holder=%s", leading, holderID) //nolint:gosec
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("leader election stopped: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		reconcileLoop(ctx, reconcileInterval, &isLeader, ghClient, cfgSrc, reconciler, dynUnits)
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	wg.Wait()
	return nil
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

func reconcileLoop(ctx context.Context, interval time.Duration, isLeader *atomic.Bool, gh *githubapp.Client, cs configSource, reconciler *reconcile.Reconciler, units *dynamicUnits) {
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

			if !isLeader.Load() {
				continue
			}
			if _, err := reconciler.RunOnce(ctx, expanded); err != nil {
				log.Printf("reconcile: run once: %v", err)
			}
		}
	}
}
