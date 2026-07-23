// Package specs provides embedded access to the honeypot configuration
// JSON Schemas. The schemas define the shared validation contract for
// Beelzebub service configurations across all platforms (Go, Java, TypeScript).
//
// The raw schema files live alongside this file and are embedded into
// the Go binary via //go:embed.
package specs

import "embed"

// FS contains all JSON Schema files in the specs/ directory.
//
//go:embed *.schema.json
var FS embed.FS
