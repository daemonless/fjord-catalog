package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool that runs and exits, an image only built FROM, or a part of a
// stack is not an app the store offers: the builder skips it by its
// x-daemonless class.
func TestToolsAndBaseImagesAreNotApps(t *testing.T) {
	for _, class := range []string{"cli", "base", "part"} {
		compose := "name: tool\nx-daemonless:\n  title: Tool\n  class: " + class + "\nservices:\n  tool:\n    image: ghcr.io/daemonless/tool:latest\n"
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
		_, err := deriveManifest([]byte(compose), nil, dir, "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "class "+class) {
			t.Errorf("class %s: want a skip naming the class, got %v", class, err)
		}
	}
	// The same compose with no class is an ordinary service and derives.
	compose := "name: app\nx-daemonless:\n  title: App\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	if _, err := deriveManifest([]byte(compose), nil, dir, "app", nil); err != nil {
		t.Errorf("a service should derive, got %v", err)
	}
}

// One service plus an example.env is an image, not a stack: it keeps its
// variants. The stack path is for composes with several services.
func TestOneServiceWithExampleEnvStaysAnImage(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    environment:\n      - APP_DB=${APP_DB:-sqlite}\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "example.env"), []byte("TZ=UTC\n"), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.xf.Info.Class == "stack" {
		t.Errorf("derived as a stack; want an image with its version picker")
	}
}

// An env value with a compose fallback is optional: the service runs
// without it, so the wizard must not demand it.
func TestComposeFallbackMakesAVariableOptional(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    environment:\n      - APP_DB_HOST=${APP_DB_HOST:-}\n      - APP_DB_TYPE=${APP_DB_TYPE:-sqlite}\n      - APP_KEY=\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := map[string]bool{}
	for _, v := range d.xf.Variables {
		got[v.Name] = v.Optional
	}
	if !got["APP_DB_HOST"] || !got["APP_DB_TYPE"] {
		t.Errorf("fallback variables should be optional, got %v", got)
	}
	if got["APP_KEY"] {
		t.Errorf("a plain empty value stays required, got %v", got)
	}
}

// A service behind a compose profile is a part on offer, not part of the
// default install: it stays out of the stack's manifest, its variables too.
func TestProfiledServicesStayOutOfTheStackManifest(t *testing.T) {
	compose := "name: photos\nx-daemonless:\n  title: Photos\n  type: stack\nservices:\n  server:\n    image: ghcr.io/daemonless/photos-server:latest\n    environment:\n      - TZ=${TZ}\n    depends_on:\n      - db\n      - proxy\n  proxy:\n    image: ghcr.io/daemonless/photos-proxy:latest\n    profiles: [proxy]\n    environment:\n      - PUBLIC_BASE_URL=${PUBLIC_BASE_URL:-}\n  db:\n    image: ghcr.io/daemonless/postgres:17\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "example.env"), []byte("TZ=UTC\n"), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "photos", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if strings.Contains(d.manifestYAML, "photos-proxy") {
		t.Errorf("the profiled service is in the manifest:\n%s", d.manifestYAML)
	}
	if strings.Contains(d.manifestYAML, "- proxy") {
		t.Errorf("the dropped service is still a depends_on")
	}
	for _, v := range d.xf.Variables {
		if v.Name == "PUBLIC_BASE_URL" {
			t.Errorf("a variable only the dropped service reads is still asked for")
		}
	}
	if !strings.Contains(d.manifestYAML, "- db") {
		t.Errorf("the other depends_on entry went missing")
	}
}

// A ${VAR} in a comment is prose, not a variable.
func TestCommentedVariableIsNotAsked(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\n  type: stack\nservices:\n  app:\n    # Tag in a ${VAR} so the wizard offers versions.\n    image: ghcr.io/daemonless/app:${APP_TAG:-latest}\n    environment:\n      - TZ=${TZ}  # or ${OTHER}\n  db:\n    image: ghcr.io/daemonless/postgres:17\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := map[string]bool{}
	for _, v := range d.xf.Variables {
		got[v.Name] = true
	}
	if got["VAR"] || got["OTHER"] {
		t.Errorf("a variable from a comment is asked for: %v", got)
	}
	if !got["APP_TAG"] || !got["TZ"] {
		t.Errorf("the compose's own variables went missing: %v", got)
	}
}

// A docs entry can mark a variable as the address the app is opened at:
// the variable becomes fjord's public_url kind.
func TestDocsPublicURL(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\n  docs:\n    env:\n      APP_URL:\n        desc: Address you open it at\n        public_url: true\n        optional: true\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    environment:\n      - APP_URL=\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	for _, v := range d.xf.Variables {
		if v.Name == "APP_URL" && (v.Type != "public_url" || !v.Optional) {
			t.Fatalf("APP_URL = %+v, want an optional public_url", v)
		}
	}
}

// fjord makes a required secret up; `generate: false` marks one only its
// owner has (cloudflared's tunnel token). A _FILE variable is the path to a
// secret, not the secret.
func TestDocsGenerateFalse(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\n  docs:\n    env:\n      TUNNEL_TOKEN:\n        desc: Your tunnel token\n        generate: false\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    environment:\n      - TUNNEL_TOKEN=\n      - AGENT_SECRET=change-me\n      - JWT_SECRET_FILE=/run/secret\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := map[string]variable{}
	for _, v := range d.xf.Variables {
		got[v.Name] = v
	}
	if v := got["TUNNEL_TOKEN"]; v.Type != "secret" || v.Generate == nil || *v.Generate {
		t.Errorf("TUNNEL_TOKEN = %+v, want a secret with generate: false", v)
	}
	if v := got["AGENT_SECRET"]; v.Type != "secret" || v.Generate != nil {
		t.Errorf("AGENT_SECRET = %+v, want a secret fjord may make up", v)
	}
	if v := got["JWT_SECRET_FILE"]; v.Type != "string" {
		t.Errorf("JWT_SECRET_FILE = %+v, want a string (a path)", v)
	}
}

// Two folders named alike keep two variables, so two folders on the host.
func TestDeriveVolumesNamedAlike(t *testing.T) {
	compose := []byte(`x-daemonless:
  title: SearXNG
services:
  searxng:
    image: ghcr.io/x/searxng:latest
    ports: ["8080:8080"]
    volumes:
      - /containers/searxng/etc:/usr/local/etc/searxng
      - /containers/searxng/cache:/var/cache/searxng
`)
	d, err := deriveManifest(compose, nil, t.TempDir(), "searxng", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"${SEARXNG_DATA}:/usr/local/etc/searxng"`, `"${CACHE_SEARXNG_DATA}:/var/cache/searxng"`} {
		if !strings.Contains(d.manifestYAML, want) {
			t.Errorf("manifest lacks %s:\n%s", want, d.manifestYAML)
		}
	}
}

// x-daemonless.name_env reaches the manifest as info.name_env: fjord fills
// those variables with the names it gives the app (SABnzbd's whitelist).
func TestNameEnvCarried(t *testing.T) {
	compose := "name: app\nx-daemonless:\n  title: App\n  name_env:\n    HOST_ALLOWLIST: \"{names}\"\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    ports: [\"8080:8080\"]\n"
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	d, err := deriveManifest([]byte(compose), nil, dir, "app", nil)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if d.xf.Info.NameEnv["HOST_ALLOWLIST"] != "{names}" || !strings.Contains(d.manifestYAML, "name_env:") {
		t.Fatalf("got %v\n%s", d.xf.Info.NameEnv, d.manifestYAML)
	}
}

// A repo with a Containerfile.linux runs on Linux too: its entry says so, so
// a Linux fjord lists it from a FreeBSD catalog.
func TestLinuxImageWidensOS(t *testing.T) {
	compose := []byte("x-daemonless:\n  title: App\nservices:\n  app:\n    image: ghcr.io/daemonless/app:latest\n    ports: [\"8080:8080\"]\n")
	dir := t.TempDir()
	d, err := deriveManifest(compose, nil, dir, "app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := catalogEntryFor(d, "app"); e.OS != nil {
		t.Fatalf("no Containerfile.linux: os %v, want the catalog's", e.OS)
	}
	_ = os.WriteFile(filepath.Join(dir, "Containerfile.linux"), []byte("FROM alpine\n"), 0o644)
	if d, err = deriveManifest(compose, nil, dir, "app", nil); err != nil {
		t.Fatal(err)
	}
	if e := catalogEntryFor(d, "app"); strings.Join(e.OS, ",") != "freebsd,linux" {
		t.Fatalf("os %v, want freebsd,linux", e.OS)
	}
}
