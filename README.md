# fjord-catalog

A builder that turns a set of container image repositories into an
app-store catalog: `catalog.json` (names, descriptions, icons, upstream
links, image refs, versions) plus an install manifest per app. It is what
publishes the [daemonless](https://github.com/daemonless) catalog that
[fjord](https://github.com/daemonless/fjord) installs from, and it is meant
to be reused: anyone can publish a catalog for their own images with two
files and no fork.

## Publish your own catalog (two files)

```
my-catalog/
├── sources.yaml                    # what to scan + branding
└── .github/workflows/catalog.yml   # calls the reusable workflow here
```

`sources.yaml`:

```yaml
catalog:
  name: "My Apps"
  maintainer: "https://example.org"
  icon: "icon.svg"                  # optional; put icon.svg next to sources.yaml

sources:
  - name: myapps                    # becomes the URL path: /v1/myapps/
    provider: daemonless-github     # list the org, shallow-clone matching repos
    org: my-github-org
    exclude: "^(base|\\.github)$"   # regexps over repo names; include: also exists
```

`.github/workflows/catalog.yml`:

```yaml
on:
  schedule: [{ cron: "0 */6 * * *" }]
  workflow_dispatch:
permissions: { contents: read, pages: write, id-token: write }
jobs:
  catalog:
    uses: daemonless/fjord-catalog/.github/workflows/build.yml@main
```

Enable GitHub Pages on the repo (Settings → Pages → Source: *GitHub
Actions*), run the workflow, and the catalog is live at
`https://<you>.github.io/<repo>/v1/<source>/catalog.json`. A `CNAME` file
in the repo gives it a custom domain. Point fjord at
`https://<host>/v1/<source>` in Settings → Catalogs.

The workflow's inputs (`sources`, `out`, `publish`, `builder-ref`, `apps`)
are documented in [`.github/workflows/build.yml`](.github/workflows/build.yml).
`sources.yaml` may live in a subdirectory (`with: { sources: catalog/sources.yaml }`);
`CNAME`, `icon.svg` and `static/` are picked up from beside it. The
daemonless catalog is exactly this: the `catalog/` directory of
[daemonless/daemonless](https://github.com/daemonless/daemonless).

## What a source needs

Each app is a repository with a `compose.yaml` carrying `x-daemonless`
metadata (title, icon, category, description, links, docs for env/ports/
volumes) and, optionally, a `.daemonless/config.yaml` and an `sbom.json`
for versions. See the daemonless image repos for the shape.

Two derivation modes, chosen per app:

- **Image mode** (default): a single-service compose gets variabilized —
  ports become `WEB_PORT`/`PORT_<n>` variables, volumes `CONFIG_DATA`/
  `<NAME>_PATH`, environment entries become variables — so an install UI
  can render a form.
- **Stack mode** (`x-daemonless: type: stack`): an authored multi-service
  compose passes through untouched; its `${VARS}` are collected into
  variables (defaults from `${VAR:-x}` fallbacks or `example.env`).

Repos opt out with `.daemonless/config.yaml` → `fjord: { exclude: true }`.

## Output

```
<out>/
├── sources.json              # [{name, catalog, apps}] — one entry per source
└── <source>/
    ├── catalog.json          # the index: one entry per app
    ├── manifests/<id>.yaml   # install manifest: compose + x-fjord block
    └── icons/<id>.svg|png    # self-hosted icons
```

All paths inside `catalog.json` (`icon`, `manifest_url`, the catalog's own
`icon`) are **relative to that catalog's base URL**, so the files can be
served from anywhere. Versions live only in `catalog.json`; manifests are
version-free so they stay byte-stable across releases. A `catalog.json`
entry:

```json
{
  "id": "radarr",
  "name": "Radarr",
  "category": "Media Management",
  "class": "service",
  "icon": "icons/radarr.svg",
  "description": "Automated movie collection manager ...",
  "upstream_url": "https://github.com/Radarr/Radarr",
  "web_url": "https://radarr.video/",
  "image": "ghcr.io/daemonless/radarr",
  "manifest_url": "manifests/radarr.yaml",
  "version": "6.3.0.10514",
  "variants": [
    {"id": "latest", "label": "Latest", "default": true,
     "image": "ghcr.io/daemonless/radarr:latest", "version": "6.3.0.10514"}
  ]
}
```

`class` is `service` (single image) or `stack` (multi-service; no
`variants`, its images are pinned by its own variables).

## Running locally

```sh
go run . --sources sources.yaml --repos-dir repos --out out/v1
go run . --sources sources.yaml --repos-dir repos --out out/v1 --apps radarr,sonarr   # a few apps
```

Flags: `--sources` (default `sources.yaml`; absent = derive `--repos-dir`
as one unbranded source), `--repos-dir` (where clones live / are made),
`--out`, `--apps`, `--versions file.json` (override sbom-derived versions).
`GITHUB_TOKEN` lifts the anonymous API rate limit and lets private repos in.

Providers: `daemonless-github` (list org + shallow clone) and `local` (a
directory of app repos already on disk, e.g. a workspace checkout).

## This repo

`ci.yml` vets, tests, and smoke-derives three real apps on every push.
The committed `catalog.json`, `manifests/` and `icons/` at the root are the
pre-split output and stay only until fjord instances have moved to the
Pages URL; they are no longer regenerated here.

License: BSD-2-Clause.
