// Package mermaidruntime embeds the pinned installation manifest independently
// of the source checkout. Importing it never installs dependencies.
package mermaidruntime

import _ "embed"

//go:embed package.json
var manifest []byte

//go:embed package-lock.json
var lockfile []byte

func Files() map[string][]byte {
	return map[string][]byte{
		"package.json":      append([]byte(nil), manifest...),
		"package-lock.json": append([]byte(nil), lockfile...),
	}
}
