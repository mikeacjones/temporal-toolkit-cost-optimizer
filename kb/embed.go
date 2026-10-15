// Package kb holds the knowledge base that ships inside the binary: billing
// rules, fix guidance, and snippets.
package kb

import "embed"

//go:embed billing guidance checks features.yaml
var FS embed.FS
