# Exclude

`apps[].exclude` (§5.1): drops specific projects out of an app's otherwise
environment-wide expansion — e.g. a batch job that only runs in one of an
environment's several regions, while every other app in that environment
still expands to all of them.

A project can't be both excluded and overridden in the same app —
`config.Parse` rejects that combination as a real config mistake (the
override would expand to nothing, unreachable), not a meaningful one.

See [`runcd.yaml`](runcd.yaml).
