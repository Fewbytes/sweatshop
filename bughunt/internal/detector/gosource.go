package detector

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// goSource is one parsed Go file. Path is slash-separated and relative to the
// tree root, matching the paths git reports.
type goSource struct {
	Path   string
	Dir    string
	IsTest bool
	Fset   *token.FileSet
	File   *ast.File
	Src    []byte
}

// goTree is every Go file under a root, plus the mapping from import path to
// root-relative directory that static-dependency checks need.
type goTree struct {
	Files           []goSource
	DirByImportPath map[string]string
}

// loadGoTree parses every .go file under root, skipping hidden directories,
// vendor and testdata. A file that does not parse is an error: a metric
// computed over a silently shrunken tree would look like an improvement.
func loadGoTree(root string) (goTree, error) {
	tree := goTree{DirByImportPath: map[string]string{}}
	modules := map[string]string{} // root-relative dir of go.mod -> module path
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case name == "go.mod":
			mod, err := readModulePath(p)
			if err != nil {
				return err
			}
			modules[path.Dir(rel)] = mod
		case strings.HasSuffix(name, ".go"):
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			tree.Files = append(tree.Files, goSource{
				Path: rel, Dir: path.Dir(rel), IsTest: strings.HasSuffix(name, "_test.go"),
				Fset: fset, File: f, Src: src,
			})
		}
		return nil
	})
	if err != nil {
		return goTree{}, err
	}
	for _, f := range tree.Files {
		if ip, ok := importPathOf(f.Dir, modules); ok {
			tree.DirByImportPath[ip] = f.Dir
		}
	}
	return tree, nil
}

// importPathOf returns the import path of a root-relative directory, derived
// from the nearest enclosing go.mod. The root is "." in path.Dir terms.
func importPathOf(dir string, modules map[string]string) (string, bool) {
	for cur := dir; ; cur = path.Dir(cur) {
		if mod, ok := modules[cur]; ok {
			if cur == dir {
				return mod, true
			}
			if cur == "." {
				return mod + "/" + dir, true
			}
			return mod + "/" + strings.TrimPrefix(dir, cur+"/"), true
		}
		if cur == "." || cur == "/" {
			return "", false
		}
	}
}

func readModulePath(goModPath string) (string, error) {
	f, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: no module directive", goModPath)
}

// importedPaths returns the unquoted import paths of f.
func importedPaths(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil {
			out = append(out, p)
		}
	}
	return out
}
