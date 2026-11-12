// Package schema embeds the JSON Schema for the slide feed, for tools and
// for connector authors working in other languages.
package schema

import _ "embed"

// V1 is the JSON Schema (draft 2020-12) for doxacast.slides/v1.
//
//go:embed doxacast.slides.v1.schema.json
var V1 []byte
