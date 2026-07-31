package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// newTabwriter matches the alignment `kubectl`/`argocd` output uses —
// tab-separated, minimum 2-space padding, no border characters.
func newTabwriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

func renderUnits(w io.Writer, units []unit) {
	if len(units) == 0 {
		_, _ = fmt.Fprintln(w, "No sync units configured.")
		return
	}
	tw := newTabwriter(w)
	_, _ = fmt.Fprintln(tw, "ENV\tAPP\tPROJECT\tSTATUS\tHEALTH\tAUTO\tCAN SYNC")
	for _, u := range units {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\t%v\n", u.Env, u.App, u.Project, u.Status, u.Health, u.Auto, u.CanSync)
	}
	_ = tw.Flush()
}

func renderUnit(w io.Writer, u unit) {
	desired := orDash(u.DesiredImage)
	live := orDash(u.LiveImage)
	lastReconciled := "never"
	if u.LastReconciledAt != nil {
		lastReconciled = *u.LastReconciledAt
	}
	_, _ = fmt.Fprintf(w, "App:             %s\n", u.App)
	_, _ = fmt.Fprintf(w, "Project:         %s\n", u.Project)
	_, _ = fmt.Fprintf(w, "Environment:     %s\n", u.Env)
	_, _ = fmt.Fprintf(w, "Region:          %s\n", u.Region)
	_, _ = fmt.Fprintf(w, "Status:          %s\n", u.Status)
	_, _ = fmt.Fprintf(w, "Health:          %s\n", u.Health)
	_, _ = fmt.Fprintf(w, "Auto-sync:       %v\n", u.Auto)
	_, _ = fmt.Fprintf(w, "Can sync (you):  %v\n", u.CanSync)
	_, _ = fmt.Fprintf(w, "Desired image:   %s\n", desired)
	_, _ = fmt.Fprintf(w, "Live image:      %s\n", live)
	_, _ = fmt.Fprintf(w, "Last reconciled: %s\n", lastReconciled)
}

func renderHistory(w io.Writer, events []syncEvent) {
	if len(events) == 0 {
		_, _ = fmt.Fprintln(w, "No sync attempts recorded yet for this unit.")
		return
	}
	tw := newTabwriter(w)
	_, _ = fmt.Fprintln(tw, "STARTED\tTRIGGER\tACTOR\tFROM\tTO\tRESULT\tERROR")
	for _, e := range events {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.StartedAt, e.Trigger, orDash(e.Actor), shortDigest(e.FromImage), shortDigest(e.ToImage), e.Result, orDash(e.Error))
	}
	_ = tw.Flush()
}

func renderRBAC(w io.Writer, rules []rbacRule) {
	if len(rules) == 0 {
		_, _ = fmt.Fprintln(w, "No roles configured — every sync request will be denied until rbac.yaml grants one.")
		return
	}
	tw := newTabwriter(w)
	_, _ = fmt.Fprintln(tw, "SUBJECT\tROLE\tSCOPE")
	for _, r := range rules {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Subject, r.Role, strings.Join(r.Scope, ","))
	}
	_ = tw.Flush()
}

func renderSyncResponse(w io.Writer, res syncResponse) {
	_, _ = fmt.Fprintf(w, "%s/%s: status=%s health=%s\n", res.Project, res.App, res.Status, res.Health)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortDigest matches web/src/components/history-table.tsx's shortDigest:
// "sha256:<8 hex chars>" instead of the full 64-char digest, since a sync
// history table needs to fit a terminal width too.
func shortDigest(digest string) string {
	if digest == "" {
		return "-"
	}
	i := strings.Index(digest, ":")
	if i == -1 || len(digest) < i+9 {
		if len(digest) > 12 {
			return digest[:12]
		}
		return digest
	}
	return digest[:i+9]
}
