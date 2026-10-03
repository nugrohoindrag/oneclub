// Package archtest enforces module boundaries (Technical Doc §4.2,
// FR-TEC-01) as a plain Go test so it runs locally and in CI:
//
//  1. kernel is the shared kernel: it imports no OneClub module.
//  2. platform never imports a business module (dependencies point down:
//     business line → shared core → back office → platform); upward
//     communication uses domain events.
//  3. business modules never import each other's code, except through the
//     public interface of the platform.
//  4. only the composition root (internal/app) and cmd wire modules together.
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

var business = []string{"golf", "sportclub", "stay", "banquet", "membership", "reservation", "commercial", "billing",
	"crm", "inventory", "procurement", "accounting", "hris", "reporting", "cms"}

func root(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// moduleOf returns "kernel", "platform", a business module name, "app" or "".
func moduleOf(importPath string) string {
	p := strings.TrimPrefix(importPath, "oneclub/internal/")
	if p == importPath {
		return ""
	}
	return strings.SplitN(p, "/", 2)[0]
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
			to := moduleOf(ip)
			if to == "" || to == from {
				continue
			}
			bad := ""
			switch {
			case to == "app":
				bad = "only cmd may import the composition root"
			case from == "kernel":
				bad = "kernel must not import OneClub modules"
			case from == "platform" && contains(business, to):
				bad = "platform must not depend on business module " + to + " (use domain events)"
			case contains(business, from) && contains(business, to):
				bad = "business module " + from + " must not import " + to + " (use its public interface via events or reporting read models)"
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

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
