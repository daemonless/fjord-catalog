# fjord-catalog

A machine-readable catalog of the daemonless container images: names,
descriptions, icons, upstream links, image refs, versions, and install
manifests. Built for app-store style consumers — anything that wants to list
or install these images can use it (dashboards, managers, front-ends).

## Consuming the catalog

Everything is static files, served from this repo:

| File | What it is |
|---|---|
| `catalog.json` | The index: one entry per app (see below) |
| `manifests/<id>.yaml` | Install manifest: the app's compose + an `x-fjord` block (variables, links) |
| `icons/<id>.svg\|png` | App icons (self-hosted where the image repo ships a logo) |

A `catalog.json` entry:

```json
{
  "id": "radarr",
  "name": "Radarr",
  "category": "Media Management",
  "class": "service",
  "icon": "/catalog/icons/radarr.svg",
  "description": "Automated movie collection manager ...",
  "upstream_url": "https://github.com/Radarr/Radarr",
  "web_url": "https://radarr.video/",
  "image": "ghcr.io/daemonless/radarr",
  "manifest_url": "/catalog/manifests/radarr.yaml",
  "version": "6.3.0.10514",
  "variants": [
    {"id": "latest", "label": "Upstream binary", "default": true,
     "image": "ghcr.io/daemonless/radarr:latest", "version": "6.3.0.10514"}
  ]
}
```

`icon`/`manifest_url` are root-relative (`/catalog/...`): resolve them against
wherever you fetched the catalog from. `class` is `service` (single image) or
`stack` (multi-service, e.g. immich). Versions live only in `catalog.json` —
manifests are version-free so they stay byte-stable across releases.

## How it's built

`main.go` is a derivation tool: pointed at a directory of image repos (each a
`compose.yaml` + optional `.daemonless/config.yaml`), it produces the catalog.
Nothing registry- or vendor-specific is hardcoded — image refs come from the
composes, versions from each repo's `sbom.json`.

Two derivation modes, chosen per app:

- **Image mode** (default): a single-service compose gets variabilized —
  ports become `WEB_PORT`/`PORT_<n>` variables, volumes become
  `CONFIG_DATA`/`<NAME>_PATH`, environment entries become variables — so the
  install UI can render a form.
- **Stack mode** (`x-daemonless: type: stack`): an authored multi-service
  compose passes through untouched; its existing `${VARS}` are collected into
  variables (defaults from inline `${VAR:-x}` fallbacks or `example.env`).

Repos opt out with `.daemonless/config.yaml`:

```yaml
fjord:
  exclude: true
```

## Running locally

```sh
go run . --repos-dir /path/to/daemonless-repos --out .
```

Flags: `--apps radarr,sonarr` limits derivation; `--versions file.json`
overrides the sbom-derived versions.

## CI

`.github/workflows/build.yml` rebuilds every 6 hours: shallow-clones the org's
repos, derives, and commits the result back when it changed.
