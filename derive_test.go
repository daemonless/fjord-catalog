package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool that runs and exits, or an image only built FROM, is not an app
// the store offers: the builder skips it by its x-daemonless class.
func TestToolsAndBaseImagesAreNotApps(t *testing.T) {
	for _, class := range []string{"cli", "base"} {
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
