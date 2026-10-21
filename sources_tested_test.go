package main

import (
	"os"
	"path/filepath"
	"testing"
)

// tested: names a file beside sources.yaml, wherever the build runs from.
func TestTestedRelativeToSources(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sources.yaml")
	os.WriteFile(p, []byte("sources:\n  - name: linuxserver\n    provider: linuxserver-api\n    tested: tested-linuxserver.json\n"), 0o644)
	c, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Sources[0].Tested; got != filepath.Join(dir, "tested-linuxserver.json") {
		t.Errorf("tested %q", got)
	}
}
