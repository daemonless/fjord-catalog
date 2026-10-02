package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Stack choices (x-daemonless.choices), as dbuild resolves them (`dbuild
// choices`): per option the env it sets, the services to add or drop, the
// values to ask for, the defaults and the secrets the extra services need.
// Carried in the manifest as x-fjord.choices for fjord to apply at install;
// dbuild owns what a database engine looks like, so the catalog never
// re-derives it.
type choiceAsk struct {
	Name    string            `yaml:"name" json:"name"`
	Label   string            `yaml:"label,omitempty" json:"label"`
	Default string            `yaml:"default,omitempty" json:"default"`
	Type    string            `yaml:"type,omitempty" json:"type"`
	Values  map[string]string `yaml:"values,omitempty" json:"values"`
}

type choiceOption struct {
	ID        string              `yaml:"id" json:"id"`
	Label     string              `yaml:"label" json:"label"`
	Doc       string              `yaml:"doc,omitempty" json:"doc"`
	Env       map[string]string   `yaml:"env,omitempty" json:"env"`
	Defaults  map[string]string   `yaml:"defaults,omitempty" json:"defaults"`
	Secrets   []string            `yaml:"secrets,omitempty" json:"secrets"`
	Services  string              `yaml:"services,omitempty" json:"services"`
	DependsOn map[string][]string `yaml:"depends_on,omitempty" json:"depends_on"`
	Drop      []string            `yaml:"drop,omitempty" json:"drop"`
	Ask       []choiceAsk         `yaml:"ask,omitempty" json:"ask"`
}

type choice struct {
	ID      string         `yaml:"id" json:"id"`
	Kind    string         `yaml:"kind,omitempty" json:"kind"`
	Label   string         `yaml:"label" json:"label"`
	Doc     string         `yaml:"doc,omitempty" json:"doc"`
	Default string         `yaml:"default" json:"default"`
	Options []choiceOption `yaml:"options" json:"options"`
}

// renderChoices asks dbuild for the repo's choices. Nil when there are none
// or when dbuild is not available (the catalog still builds).
func renderChoices(repoDir string) ([]choice, error) {
	if appjailDbuildPath == "" {
		return nil, nil
	}
	// To a file: dbuild logs to stdout (a stack repo warns about having no
	// variants before any command runs).
	tmp, err := os.CreateTemp("", "choices-*.json")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command("python3", "-m", "dbuild", "choices", "--out", tmp.Name())
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+appjailDbuildPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("dbuild choices in %s: %v: %s", repoDir, err, out)
	}
	out, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	var data struct {
		Choices []choice `json:"choices"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("dbuild choices in %s: %w", repoDir, err)
	}
	return data.Choices, nil
}
