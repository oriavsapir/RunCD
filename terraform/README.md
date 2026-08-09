# terraform/

Two independent, sibling modules — not one thing split in half, and not a
parent/child pair. Apply them in this order:

## 1. [`controller`](controller) — apply first

Everything runcd's controller (and, optionally, its dashboard) needs to
exist and run: the controller's shared service account, its IAM grants
across every project/folder it manages, API enablement, Secret Manager
access, Cloud SQL IAM auth — and, if you want it, the Cloud Run v2 services
themselves for the controller and dashboard. This is the module a new
deployment imports.

## 2. [`image-events`](image-events) — apply second, optional

A small Eventarc add-on that nudges the controller to reconcile sooner after
an Artifact Registry push, instead of waiting out `RECONCILE_INTERVAL`. It
has to come second because it reads the controller's *already-deployed*
Cloud Run service (via a data source) to build its trigger destination —
it can't exist before the controller does. Skip it entirely if the default
polling interval is fine.

## Why two modules, not one

They were nested at one point (`image-events` as a submodule under
`controller`) and that was reverted: nesting implies a parent/child
relationship — "an Eventarc trigger" as a component *of* "the controller
module" — that isn't true. The real relationship is an apply-order
dependency between two independently-lifecycled root modules, which is what
"two sibling directories, applied in order, documented here" actually says.
