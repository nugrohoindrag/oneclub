package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// rowMappers map a database row onto a struct by column name.
var rowMappers = map[string]map[string]bool{
	"handle": {"List": true, "One": true, "Get": true},
	"pgx":    {"RowToStructByName": true, "RowToStructByNameLax": true, "RowToAddrOfStructByName": true, "RowToAddrOfStructByNameLax": true},
}

// TestRowStructsHaveDBTags: every exported field of a struct filled from a
// row by name carries a db tag (db:"-" for a field set in code). Without it
// pgx matches the Go field name, which garble renames in the obfuscated VPS
// build — the query then fails there only ("struct doesn't have corresponding
// row field …"), while the readable CI build passes.
func TestRowStructsHaveDBTags(t *testing.T) {
	base := filepath.Join(root(t), "internal")
	type pkgFile struct {
		dir string
		f   *ast.File
	}
	var files []pkgFile
	structs := map[string]*ast.StructType{} // dir + "." + type name
	fset := token.NewFileSet()
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		files = append(files, pkgFile{dir, f})
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					structs[dir+"."+ts.Name.Name] = st
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, pf := range files {
		imports := map[string]string{} // package name → directory
		for _, im := range pf.f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			rel, ok := strings.CutPrefix(p, "oneclub/internal/")
			if !ok {
				continue
			}
			name := filepath.Base(rel)
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = filepath.Join(base, filepath.FromSlash(rel))
		}
		// resolve returns the struct of a type expression (same package or an imported OneClub package).
		resolve := func(e ast.Expr) (string, *ast.StructType) {
			if s, ok := e.(*ast.StarExpr); ok {
				e = s.X
			}
			key := ""
			switch x := e.(type) {
			case *ast.Ident:
				key = pf.dir + "." + x.Name
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && imports[pkg.Name] != "" {
					key = imports[pkg.Name] + "." + x.Sel.Name
				}
			}
			return key, structs[key]
		}
		ast.Inspect(pf.f, func(n ast.Node) bool {
			ix, ok := n.(*ast.IndexExpr)
			if !ok {
				return true
			}
			sel, ok := ix.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || !rowMappers[pkg.Name][sel.Sel.Name] {
				return true
			}
			key, st := resolve(ix.Index)
			if st == nil {
				return true
			}
			checked++
			for _, fld := range st.Fields.List {
				if len(fld.Names) == 0 {
					continue // embedded: pgx maps its fields, checked where it is mapped itself
				}
				tag := ""
				if fld.Tag != nil {
					tag, _ = strconv.Unquote(fld.Tag.Value)
				}
				for _, name := range fld.Names {
					if !name.IsExported() {
						continue
					}
					if _, ok := reflect.StructTag(tag).Lookup("db"); !ok {
						rel, _ := filepath.Rel(root(t), fset.Position(name.Pos()).Filename)
						t.Errorf("%s:%d: %s.%s is filled from a row by name (%s.%s) but has no db tag; add db:\"column\" or db:\"-\"",
							filepath.ToSlash(rel), fset.Position(name.Pos()).Line, filepath.Base(key), name.Name, sel.X.(*ast.Ident).Name, sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no row-mapped struct found: the scan is broken")
	}
}
