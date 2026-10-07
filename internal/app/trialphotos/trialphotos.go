// Package trialphotos holds the product photos of the trial dataset, one
// <product code>.webp per trial product (sources and licenses: CREDITS.md).
package trialphotos

import "embed"

// FS holds the photos.
//
//go:embed *.webp
var FS embed.FS
