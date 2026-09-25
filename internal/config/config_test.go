package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXDGAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("LAZYMERMAID_THEME", "neutral")
	path := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(path, []byte("[preview]\ntheme = 'dark'\n[scan]\nhidden = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Preview.Theme != "neutral" || !c.Scan.Hidden {
		t.Fatalf("precedence: %+v", c)
	}
	if _, err = Load(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("explicit missing config must fail")
	}
	if _, err = Load(""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if !filepath.IsAbs(DefaultPath()) {
		t.Fatal("relative XDG value accepted")
	}
}
