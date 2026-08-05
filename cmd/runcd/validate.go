package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"gopkg.in/yaml.v3"

	"github.com/runcd/runcd/internal/config"
	"github.com/runcd/runcd/internal/expander"
	"github.com/runcd/runcd/internal/folders"
	"github.com/runcd/runcd/internal/manifest"
	"github.com/runcd/runcd/internal/notify"
	"github.com/runcd/runcd/internal/rbac"
)

// strictDecode re-decodes data with KnownFields(true) — every real Parse
// function (config.Parse, rbac.Parse, manifest.Parse) uses a plain
// yaml.Unmarshal deliberately, so a hot-reloaded controller doesn't hard-fail
// on a field it doesn't recognize yet (a deprecated key mid-rollout, an
// operator's stray comment-turned-key). That leniency also means a genuine
// typo — "intervl" for "interval", "blabla: 300" — silently parses as
// nothing rather than erroring. validate runs offline against a human before
// anything is committed, exactly where being pedantic about that costs
// nothing, so it re-decodes strictly as a second pass on top of the normal
// (lenient) Parse call.
func strictDecode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// validateRepo runs the same parse/expand validation the controller does at
// boot and on every config reload (config.Parse -> expander.Expand,
// rbac.Parse), plus a best-effort manifest.Parse for every app's service
// manifest — entirely against local files, no GitHub/GCP calls, so it works
// against a checked-out manifest repo before anything is pushed.
// checkGCP/checkSlack opt into real network calls (Application Default
// Credentials for GCP, an actual webhook POST for Slack) on top of that —
// off by default, since the base validation is meant to run offline in a
// pre-commit hook or CI without cloud credentials. Returns false if
// anything failed; details go to stdout either way (errors aren't
// exceptional here, they're the result being reported).
func validateRepo(ctx context.Context, dir string, checkGCP, checkSlack bool, stdout io.Writer) bool {
	ok := true
	report := func(pass bool, format string, args ...any) {
		if !pass {
			ok = false
			_, _ = fmt.Fprintf(stdout, "FAIL  %s\n", fmt.Sprintf(format, args...))
			return
		}
		_, _ = fmt.Fprintf(stdout, "OK    %s\n", fmt.Sprintf(format, args...))
	}

	configPath := filepath.Join(dir, "runcd.yaml")
	data, err := os.ReadFile(configPath) //nolint:gosec
	if err != nil {
		report(false, "read %s: %v", configPath, err)
		return ok
	}
	root, err := config.Parse(data)
	if err != nil {
		report(false, "parse %s: %v", configPath, err)
		return ok
	}
	report(true, "%s parses", configPath)

	if err := strictDecode(data, &config.Root{}); err != nil {
		report(false, "%s has an unrecognized or misspelled field: %v", configPath, err)
	} else {
		report(true, "%s has no unrecognized fields", configPath)
	}

	var folderIDs []string
	for envName, env := range root.Environments {
		if len(env.Folders) == 0 {
			continue
		}
		folderIDs = append(folderIDs, env.Folders...)
		if !checkGCP {
			_, _ = fmt.Fprintf(stdout, "NOTE  environments.%s.folders declared but not resolved here (needs --check-gcp, a live GCP Cloud Resource Manager call) — its projects aren't checked\n", envName)
		}
	}

	units, err := expander.Expand(root)
	if err != nil {
		report(false, "expand %s: %v", configPath, err)
	} else {
		report(true, "%s expands to %d sync unit(s)", configPath, len(units))
	}

	rbacPath := filepath.Join(dir, "rbac.yaml")
	if rbacData, err := os.ReadFile(rbacPath); err != nil { //nolint:gosec
		_, _ = fmt.Fprintf(stdout, "NOTE  %s not found — a controller without it denies every manual sync\n", rbacPath)
	} else if _, err := rbac.Parse(rbacData); err != nil {
		report(false, "parse %s: %v", rbacPath, err)
	} else {
		report(true, "%s parses", rbacPath)
		if err := strictDecode(rbacData, &rbac.Config{}); err != nil {
			report(false, "%s has an unrecognized or misspelled field: %v", rbacPath, err)
		} else {
			report(true, "%s has no unrecognized fields", rbacPath)
		}
	}

	// Dedupe by SourcePath: every environment an app targets shares one
	// manifest, so validating it once per unit would just repeat the same
	// result.
	seen := make(map[string]bool)
	var paths []string
	for _, u := range units {
		if !seen[u.SourcePath] {
			seen[u.SourcePath] = true
			paths = append(paths, u.SourcePath)
		}
	}
	sort.Strings(paths)

	for _, p := range paths {
		local := filepath.Join(dir, p)
		data, err := os.ReadFile(local) //nolint:gosec
		if err != nil {
			_, _ = fmt.Fprintf(stdout, "NOTE  %s not found locally — skipped (manifests can live in a separate repo from runcd.yaml)\n", local)
			continue
		}
		if _, err := manifest.Parse(data); err != nil {
			report(false, "parse %s: %v", local, err)
			continue
		}
		report(true, "%s parses", local)
	}

	if checkGCP {
		if !checkProjectsAndFolders(ctx, units, folderIDs, report) {
			ok = false
		}
	}
	if checkSlack {
		if !checkSlackSinks(ctx, root, report) {
			ok = false
		}
	}

	return ok
}

// checkProjectsAndFolders confirms every project a unit targets, and every
// folder an environment declares, is a real, reachable resource under
// whatever credentials this process is running as (Application Default
// Credentials — the same auth flavor internal/folders.GCPResolver uses
// server-side) — a project ID typo'd in runcd.yaml passes config.Parse and
// expander.Expand cleanly (they're just strings) and would otherwise only
// surface once the controller tries to actually deploy to it.
func checkProjectsAndFolders(ctx context.Context, units []expander.SyncUnit, folderIDs []string, report func(bool, string, ...any)) bool {
	ok := true

	seen := make(map[string]bool)
	var projectIDs []string
	for _, u := range units {
		if !seen[u.Project] {
			seen[u.Project] = true
			projectIDs = append(projectIDs, u.Project)
		}
	}
	sort.Strings(projectIDs)

	if len(projectIDs) > 0 {
		pc, err := resourcemanager.NewProjectsClient(ctx)
		if err != nil {
			report(false, "create Cloud Resource Manager client: %v", err)
			return false
		}
		defer func() { _ = pc.Close() }()

		for _, id := range projectIDs {
			_, err := pc.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + id})
			if err != nil {
				ok = false
				report(false, "project %q: %v", id, err)
				continue
			}
			report(true, "project %q exists and is reachable", id)
		}
	}

	if len(folderIDs) > 0 {
		sort.Strings(folderIDs)
		fr, err := folders.NewGCPResolver(ctx)
		if err != nil {
			report(false, "create folders resolver: %v", err)
			return false
		}
		defer func() { _ = fr.Close() }()

		seenFolder := make(map[string]bool)
		for _, id := range folderIDs {
			if seenFolder[id] {
				continue
			}
			seenFolder[id] = true
			if _, err := fr.ProjectsInFolder(ctx, id); err != nil {
				ok = false
				report(false, "folder %q: %v", id, err)
				continue
			}
			report(true, "folder %q exists and is reachable", id)
		}
	}

	return ok
}

// checkSlackSinks sends a real, clearly-labeled test message through every
// configured notify.slack webhook — the only way to actually confirm a
// webhook URL is live, since a bad or revoked one still parses as a
// perfectly valid string in runcd.yaml.
func checkSlackSinks(ctx context.Context, root *config.Root, report func(bool, string, ...any)) bool {
	if len(root.Notify.Slack) == 0 {
		return true
	}
	ok := true

	names := make([]string, 0, len(root.Notify.Slack))
	for name := range root.Notify.Slack {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sink := &notify.SlackSink{WebhookURL: root.Notify.Slack[name]}
		err := sink.Send(ctx, "runcd validate --check-slack: this is a test message confirming the \""+name+"\" webhook is configured correctly. No action needed.")
		if err != nil {
			ok = false
			report(false, "slack sink %q: %v", name, err)
			continue
		}
		report(true, "slack sink %q accepted a test message", name)
	}

	return ok
}
