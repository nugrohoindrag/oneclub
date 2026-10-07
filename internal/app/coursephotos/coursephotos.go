// Package coursephotos holds the course pictures of the demo club (Modern
// Golf & Country Club): the overview course map and one hole card per hole
// (hole-NN.jpg). Source: CREDITS.md.
package coursephotos

import "embed"

// FS holds the pictures.
//
//go:embed *.jpg
var FS embed.FS
