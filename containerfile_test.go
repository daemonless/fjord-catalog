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
	compose, err := composeFromContainerfile(repo, "gitea")
	if err != nil {
		t.Fatal(err)
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

// No EXPOSE: a base or builder image, nothing to open.
func TestComposeFromContainerfileRefusesNoPort(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "Containerfile"), []byte("FROM x\nLABEL org.opencontainers.image.title=\"Core\"\n"), 0o644)
	if _, err := composeFromContainerfile(repo, "core"); err == nil || !strings.Contains(err.Error(), "EXPOSE") {
		t.Errorf("err = %v", err)
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
