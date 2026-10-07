package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// dbuild's AppJail form reaches the manifest; dropped, fjord refuses the
// option on AppJail.
func TestChoiceAppjailCarried(t *testing.T) {
	in := `{"id":"database","options":[{"id":"mariadb","label":"MariaDB",
		"appjail":{"director":"services:\n  app-mariadb:\n    name: m\n","files":{"t.conf":"x"},"hostnames":{"app-mariadb":"DB_HOST"}}}]}`
	var c choice
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"appjail:", "app-mariadb:", "t.conf: x", "app-mariadb: DB_HOST"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
