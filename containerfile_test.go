package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const giteaContainerfile = `ARG FREEBSD_RELEASE
FROM ghcr.io/appjail-makejails/core:${FREEBSD_RELEASE}
LABEL org.opencontainers.image.title="Gitea" \
    org.opencontainers.image.description="Compact self-hosted Git service" \
    org.opencontainers.image.url="https://github.com/AppJail-makejails/gitea"
# a comment
EXPOSE 22 3000
VOLUME ["/data"]
`

func TestParseContainerfile(t *testing.T) {
	cf := parseContainerfile(giteaContainerfile)
	if cf.labels["org.opencontainers.image.title"] != "Gitea" || cf.labels["org.opencontainers.image.description"] != "Compact self-hosted Git service" {
		t.Errorf("labels = %v", cf.labels)
	}
	if !reflect.DeepEqual(cf.expose, []string{"22", "3000"}) || !reflect.DeepEqual(cf.volumes, []string{"/data"}) {
		t.Errorf("expose %v volumes %v", cf.expose, cf.volumes)
	}
}

// The port people open goes first, where the deriver takes WEB_PORT from.
func TestWebFirst(t *testing.T) {
	for _, c := range []struct {
		ports []string
		cit   int
		want  []string
	}{
		{[]string{"22", "3000"}, 0, []string{"3000", "22"}},
		{[]string{"80", "443"}, 0, []string{"80", "443"}},
		{[]string{"53/udp", "80", "3000"}, 3000, []string{"3000", "53/udp", "80"}},
		{[]string{"22"}, 0, []string{"22"}},
		{[]string{"53", "53/udp", "67/udp", "80", "3000"}, 0, []string{"80", "53", "53/udp", "67/udp", "3000"}},
		{[]string{"5000/udp", "8080"}, 0, []string{"8080", "5000/udp"}},
		{[]string{"1025", "8025"}, 0, []string{"8025", "1025"}},
		{[]string{"5432"}, 0, []string{"5432"}},
	} {
		if got := webFirst(c.ports, c.cit); !reflect.DeepEqual(got, c.want) {
			t.Errorf("webFirst(%v, %d) = %v, want %v", c.ports, c.cit, got, c.want)
		}
	}
}

func TestComposeFromContainerfile(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "gitea")
	os.MkdirAll(filepath.Join(repo, ".daemonless"), 0o755)
	os.WriteFile(filepath.Join(repo, "Containerfile"), []byte(giteaContainerfile), 0o644)
	os.WriteFile(filepath.Join(repo, ".daemonless/config.yaml"), []byte("build:\n  variants:\n    - tag: \"15.1\"\n      default: true\n      aliases: [latest]\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/AppJail-makejails/gitea.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	compose, web, err := composeFromContainerfile(repo, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if web != 3000 {
		t.Errorf("web = %d, want 3000", web)
	}
	d, err := deriveManifest(compose, nil, repo, "gitea", nil)
	if err != nil {
		t.Fatalf("derive: %v\n%s", err, compose)
	}
	m := d.manifestYAML
	for _, want := range []string{
		"image: ghcr.io/appjail-makejails/gitea:15.1",
		`"${WEB_PORT}:3000"`,
		`"${PORT_22}:22"`,
		"name: Gitea",
		"description: Compact self-hosted Git service",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest lacks %q:\n%s", want, m)
		}
	}
}

// No EXPOSE still derives: the app runs, there is just no port to open.
func TestComposeFromContainerfileWithoutPort(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "ntfy")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "Containerfile"), []byte("LABEL org.opencontainers.image.title=\"ntfy\"\n"), 0o644)
	exec.Command("git", "-C", repo, "init", "-q").Run()
	exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/AppJail-makejails/ntfy.git").Run()
	compose, web, err := composeFromContainerfile(repo, "ntfy")
	if err != nil {
		t.Fatal(err)
	}
	if web != 0 || strings.Contains(string(compose), "ports") {
		t.Errorf("compose has ports:\n%s", compose)
	}
	if _, err := deriveManifest(compose, nil, repo, "ntfy", nil); err != nil {
		t.Fatal(err)
	}
}

// Ports and volumes named through ARG/ENV defaults resolve; one that does not
// is dropped rather than written as "$PORT".
func TestParseContainerfileExpandsVariables(t *testing.T) {
	cf := parseContainerfile("ARG DATA=/var/db/app\nENV PORT=3000\nENV LEGACY 8080\nEXPOSE $PORT ${LEGACY}/tcp $UNSET\nVOLUME ${DATA}\n")
	if !reflect.DeepEqual(cf.expose, []string{"3000", "8080/tcp"}) || !reflect.DeepEqual(cf.volumes, []string{"/var/db/app"}) {
		t.Errorf("expose %v volumes %v", cf.expose, cf.volumes)
	}
}

// A repo that builds only Containerfile.<variant> is read through its
// default variant's file.
func TestComposeFromVariantContainerfile(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "nextcloud")
	os.MkdirAll(filepath.Join(repo, ".daemonless"), 0o755)
	os.WriteFile(filepath.Join(repo, "Containerfile.apache"), []byte("LABEL org.opencontainers.image.title=\"Nextcloud\"\nEXPOSE 80\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "Containerfile.fpm"), []byte("LABEL org.opencontainers.image.title=\"Nextcloud\"\nEXPOSE 9000\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".daemonless/config.yaml"), []byte("build:\n  variants:\n    - tag: 15.1-apache\n      containerfile: Containerfile.apache\n      default: true\n    - tag: 15.1-fpm\n      containerfile: Containerfile.fpm\n"), 0o644)
	exec.Command("git", "-C", repo, "init", "-q").Run()
	exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/AppJail-makejails/nextcloud.git").Run()
	if !hasContainerfile(repo) {
		t.Fatal("hasContainerfile = false")
	}
	compose, _, err := composeFromContainerfile(repo, "nextcloud")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ghcr.io/appjail-makejails/nextcloud:15.1-apache", "80:80"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("compose lacks %q:\n%s", want, compose)
		}
	}
}

// A web port gets the copy's config marked, which is what gives the app an
// Open button; a database port does not.
func TestContainerfileRepoMarksWebUI(t *testing.T) {
	for _, c := range []struct {
		expose string
		web    bool
	}{{"5000", true}, {"5432", false}} {
		repo := filepath.Join(t.TempDir(), "app")
		os.MkdirAll(repo, 0o755)
		os.WriteFile(filepath.Join(repo, "Containerfile"), []byte("LABEL org.opencontainers.image.title=\"App\"\nEXPOSE "+c.expose+"\n"), 0o644)
		exec.Command("git", "-C", repo, "init", "-q").Run()
		exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/x/app.git").Run()
		dir, parent, compose, err := containerfileRepo(repo, "app")
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := os.ReadFile(filepath.Join(dir, ".daemonless/config.yaml"))
		d, err := deriveManifest(compose, cfg, dir, "app", nil)
		os.RemoveAll(parent)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.xf.Info.WebPort == "${WEB_PORT}"; got != c.web {
			t.Errorf("EXPOSE %s: web_port %q, want web %v", c.expose, d.xf.Info.WebPort, c.web)
		}
	}
}
