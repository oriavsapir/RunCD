# Region and image overrides

`apps[].overrides` (§5.1): per-project deviations from an app's shared
defaults, without needing a separate manifest file.

**Region** resolves in this order, first one set wins:

1. `overrides[project].region`
2. `environments[env].region`
3. `defaults.region`

`expander.Expand` fails loudly if none of the three apply to a given
project — there's no silent fallback to an empty region.

**Track/Version** override the app's manifest-declared `image.track`/
`image.version` for one project only (mutually exclusive, like the
manifest's own pair). They're resolved live against Artifact Registry at
reconcile time (`internal/registry`), never committed to the manifest — so
the same `service.yaml` can serve one project riding the manifest's own
pinned digest, and another tracking a different version/track live.

See [`runcd.yaml`](runcd.yaml).
