# Folders

`environments[env].folders` (§7): a list of GCP folder IDs whose direct
child projects are resolved and merged into `environments[env].projects` at
load time, deduped.

- The resolution itself is a live Cloud Resource Manager API call, done by
  `internal/folders`, not `config.Parse` — `Parse` never does I/O, so it
  just stores the folder IDs as declared.
- Only direct children are resolved — a folder's own sub-folders are not
  recursed into.
- A project can be listed explicitly in `projects` *and* discovered via a
  folder; duplicates are deduped.

See [`runcd.yaml`](runcd.yaml).
