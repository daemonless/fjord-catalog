package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// githubProvider discovers apps by listing a GitHub org's repositories and
// shallow-cloning the ones that pass the include/exclude filters into
// reposDir; derivation is then the plain daemonless provider over that dir.
// This is what lets an instance be two files (sources.yaml + a workflow):
// nothing outside the tool has to know how to enumerate the org.
type githubProvider struct {
	org     string
	include *regexp.Regexp
	exclude *regexp.Regexp
	apps    []string // explicit app ids (from --apps), or nil for the whole org
	inner   *daemonlessProvider
}

// Discover lists the org (paginated, public repos, archived ones skipped),
// clones what's missing, and hands the resulting id set to the daemonless
// provider so versions load and Derive works unchanged.
func (p *githubProvider) Discover() ([]AppRef, error) {
	names, err := listOrgRepos(p.org)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, a := range p.apps {
		want[strings.TrimSpace(a)] = true
	}
	var ids []string
	for _, n := range names {
		if len(want) > 0 && !want[n] {
			continue
		}
		if p.include != nil && !p.include.MatchString(n) {
			continue
		}
		if p.exclude != nil && p.exclude.MatchString(n) {
			continue
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no repositories in org %q match the source filters", p.org)
	}
	if err := os.MkdirAll(p.inner.reposDir, 0o755); err != nil {
		return nil, err
	}
	for _, n := range ids {
		dir := filepath.Join(p.inner.reposDir, n)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			continue // already cloned (a re-run, or the CI checkout)
		}
		url := "https://github.com/" + p.org + "/" + n + ".git"
		cmd := exec.Command("git", "clone", "--depth", "1", "--quiet", url, dir)
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			// Token in the header, never in the URL (it would leak into the
			// remote config and logs). GitHub's git endpoint rejects "bearer";
			// it wants basic auth with the x-access-token user, as actions/checkout sends.
			cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
			cmd.Args = append([]string{cmd.Args[0], "-c", "http.extraheader=AUTHORIZATION: basic " + cred}, cmd.Args[1:]...)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "  warn %s: clone: %v: %s\n", n, err, strings.TrimSpace(string(out)))
		}
	}
	p.inner.apps = ids
	return p.inner.Discover()
}

func (p *githubProvider) Derive(ref AppRef) (*DerivedApp, error) { return p.inner.Derive(ref) }

// listOrgRepos returns the org's non-archived repository names via the REST
// API (100 per page, following pages until short). GITHUB_TOKEN, when set,
// lifts the anonymous rate limit and sees private repos.
func listOrgRepos(org string) ([]string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var names []string
	for page := 1; page <= 20; page++ {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&page=%d", org, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list %s repos: %w", org, err)
		}
		var repos []struct {
			Name     string `json:"name"`
			Archived bool   `json:"archived"`
		}
		err = json.NewDecoder(resp.Body).Decode(&repos)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list %s repos: %s", org, resp.Status)
		}
		if err != nil {
			return nil, fmt.Errorf("list %s repos: decode: %w", org, err)
		}
		for _, r := range repos {
			if !r.Archived {
				names = append(names, r.Name)
			}
		}
		if len(repos) < 100 {
			break
		}
	}
	return names, nil
}
