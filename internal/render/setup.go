package render

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	mermaidruntime "lazymermaid/runtime/mermaid"
)

// PrepareRuntime writes the shipped npm manifest and lockfile into an explicit
// installation directory. Call only for an install/setup action, never from
// doctor or startup. It does not execute npm, download anything, or replace an
// existing different manifest. The user can then run npm ci in this directory.
func PrepareRuntime(dir string) error {
	files := mermaidruntime.Files()
	for name, expected := range files {
		existing, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil && !bytes.Equal(existing, expected) {
			return fmt.Errorf("%s already contains a different %s; use a separate runtime directory", dir, name)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for name, data := range files {
		if err := atomicWrite(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}
