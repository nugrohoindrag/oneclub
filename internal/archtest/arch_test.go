// Package archtest enforces module boundaries (Technical Doc §4.2,
// FR-TEC-01) as a plain Go test so it runs locally and in CI:
//
//  1. kernel is the shared kernel: it imports no OneClub module.
//  2. platform never imports a business module (dependencies point down:
//     business line → shared core → customer → back office → platform);
//     upward communication uses domain events.
//  3. a business module may only call another module through its public
//     interface — the module's root package (api.go) — never its
//     sub-packages, and only in the downward direction of the layers
//     (Technical Doc §4.2 rule 1 and 3).
//  4. reporting (Management & BI) imports no business module: it reads
//     other domains only through read models in the reporting schema.
//  5. only the composition root (internal/app) and cmd wire modules together.
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// layer of every business module (Technical Doc §4.1); a module may depend
// on modules of the same or a higher number (lower layer) only.
var layer = map[string]int{
	"golf": 1, "sportclub": 1, "stay": 1, "banquet": 1, "cms": 1,
	"membership": 2, "reservation": 2, "commercial": 2, "billing": 2,
	"crm":       3,
	"inventory": 4, "procurement": 4, "accounting": 4, "hris": 4,
	"reporting": 5,
}

func root(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// moduleOf returns "kernel", "platform", a business module name, "app" or "".
func moduleOf(importPath string) (string, bool) {
	p := strings.TrimPrefix(importPath, "oneclub/internal/")
	if p == importPath {
		return "", false
	}
	parts := strings.SplitN(p, "/", 2)
	return parts[0], len(parts) == 1
}

func TestModuleBoundaries(t *testing.T) {
	base := filepath.Join(root(t), "internal")
	fset := token.NewFileSet()
	violations := 0
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		from := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if from == "app" || from == "archtest" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			to, rootPkg := moduleOf(ip)
			if to == "" || to == from {
				continue
			}
			fromL, fromBusiness := layer[from]
			toL, toBusiness := layer[to]
			bad := ""
			switch {
			case to == "app":
				bad = "only cmd may import the composition root"
			case from == "kernel":
				bad = "kernel must not import OneClub modules"
			case from == "platform" && toBusiness:
				bad = "platform must not depend on business module " + to + " (use domain events)"
			case from == "reporting" && toBusiness:
				bad = "reporting reads other domains through reporting read models only"
			case fromBusiness && toBusiness && !rootPkg:
				bad = "business module " + from + " may only use the public interface (root package) of " + to
			case fromBusiness && toBusiness && toL < fromL:
				bad = "business module " + from + " must not depend upward on " + to + " (use domain events)"
			}
			if bad != "" {
				violations++
				t.Errorf("%s imports %s: %s", filepath.ToSlash(rel), ip, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if violations == 0 {
		t.Logf("module boundaries respected")
	}
}
