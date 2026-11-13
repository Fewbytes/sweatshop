package purity

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
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// qualifiedCall is a call written pkg.Name(...) where pkg is an import.
type qualifiedCall struct{ importPath, name string }

// funcFacts is what one function's body says on its own, before taint
// propagation. Calls are candidates: only those naming a function of the
// analysed modules become edges.
type funcFacts struct {
	id      string
	pkg     string
	callIDs []string
	qcalls  []qualifiedCall
	direct  map[Source]string // first evidence per source
	// assignedGlobals are package variables the body assigns; readGlobals every
	// package variable it mentions. A variable counts as mutable state only if
	// some function of the package assigns it.
	assignedGlobals []string
	readGlobals     []string
}

// packageUnit is one directory's non-test Go files.
type packageUnit struct {
	importPath string
	name       string
	imports    []string
	funcs      []*funcFacts
}

var versionSuffix = regexp.MustCompile(`^v[0-9]+$`)

// importName is the identifier an import without an explicit name binds.
func importName(importPath string) string {
	elems := strings.Split(importPath, "/")
	last := elems[len(elems)-1]
	if versionSuffix.MatchString(last) && len(elems) > 1 {
		return elems[len(elems)-2]
	}
	return last
}

// skipDir reports directories never analysed: vendored, generated fixtures
// and hidden ones.
func skipDir(name string) bool {
	return name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")
}

// findModules maps each go.mod directory under root to its module path.
func findModules(root string) (map[string]string, error) {
	modules := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "go.mod" {
			return nil
		}
		modPath, err := readModulePath(p)
		if err != nil {
			return err
		}
		modules[filepath.Dir(p)] = modPath
		return nil
	})
	return modules, err
}

func readModulePath(goMod string) (string, error) {
	f, err := os.Open(goMod)
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
	return "", fmt.Errorf("%s: no module directive", goMod)
}

// scanPackages parses the non-test Go files under root, grouped by directory
// and named by their module's import path. Build constraints are ignored, so
// a function defined per-platform is analysed as the union of its variants.
func scanPackages(root string) ([]*packageUnit, error) {
	modules, err := findModules(root)
	if err != nil {
		return nil, err
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("no go.mod under %s", root)
	}
	var units []*packageUnit
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		unit, err := scanDir(p, modules)
		if err != nil {
			return err
		}
		if unit != nil {
			units = append(units, unit)
		}
		return nil
	})
	return units, err
}

func scanDir(dir string, modules map[string]string) (*packageUnit, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, m := range matches {
		if !strings.HasSuffix(m, "_test.go") {
			files = append(files, m)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	importPath, err := importPathOf(dir, modules)
	if err != nil {
		return nil, err
	}
	unit := &packageUnit{importPath: importPath}
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, file)
		unit.name = file.Name.Name
	}
	pkgVars := map[string]bool{}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
				for _, spec := range gen.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						pkgVars[name.Name] = true
					}
				}
			}
		}
	}
	for _, file := range parsed {
		aliases := importAliases(file)
		for _, imp := range aliases {
			if !slices.Contains(unit.imports, imp) {
				unit.imports = append(unit.imports, imp)
			}
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && fn.Name.Name != "init" {
				unit.funcs = append(unit.funcs, scanFunc(fn, unit.importPath, aliases, pkgVars))
			}
		}
	}
	slices.Sort(unit.imports)
	return unit, nil
}

// importPathOf names dir by the nearest enclosing module.
func importPathOf(dir string, modules map[string]string) (string, error) {
	for cur := dir; ; cur = filepath.Dir(cur) {
		if modPath, ok := modules[cur]; ok {
			rel, err := filepath.Rel(cur, dir)
			if err != nil {
				return "", err
			}
			return path.Join(modPath, filepath.ToSlash(rel)), nil
		}
		if cur == filepath.Dir(cur) {
			return "", fmt.Errorf("%s is under no go.mod", dir)
		}
	}
}

// importAliases maps the identifier each import binds in file to its path.
// Dot and blank imports bind nothing a call can name.
func importAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := importName(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			aliases[name] = importPath
		}
	}
	return aliases
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// assignmentRoot is the identifier an assignment target writes through:
// x for x, x.f, x[i], *x.
func assignmentRoot(expr ast.Expr) (root *ast.Ident, bare bool) {
	bare = true
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e, bare
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		default:
			return nil, false
		}
		bare = false
	}
}

// isReferenceType reports whether a parameter of this type lets the callee
// change the caller's data.
func isReferenceType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.StarExpr, *ast.MapType:
		return true
	case *ast.ArrayType:
		return t.Len == nil
	}
	return false
}

// scanFunc reads one function body. Names are resolved by the parser's
// file-scope objects: an identifier whose declaration lies inside fn is local.
// Calls through interface values and function-typed parameters are not
// followed; they are the injection seam, so a function that receives its
// clock as an argument is deterministic.
func scanFunc(fn *ast.FuncDecl, pkgPath string, aliases map[string]string, pkgVars map[string]bool) *funcFacts {
	id := pkgPath + "." + fn.Name.Name
	recvType := receiverTypeName(fn)
	if recvType != "" {
		id = pkgPath + "." + recvType + "." + fn.Name.Name
	}
	facts := &funcFacts{id: id, pkg: pkgPath, direct: map[Source]string{}}
	isLocal := func(ident *ast.Ident) bool {
		return ident.Obj != nil && fn.Pos() <= ident.Obj.Pos() && ident.Obj.Pos() < fn.End()
	}
	isReferenceParam := func(ident *ast.Ident) bool {
		field, ok := ident.Obj.Decl.(*ast.Field)
		return ok && field.Pos() >= fn.Type.Params.Pos() && field.End() <= fn.Type.Params.End() && isReferenceType(field.Type)
	}
	addDirect := func(src Source, evidence string) {
		if _, seen := facts.direct[src]; !seen {
			facts.direct[src] = evidence
		}
	}
	noteAssignment := func(target ast.Expr) {
		root, bare := assignmentRoot(target)
		if root == nil || root.Name == "_" {
			return
		}
		if isLocal(root) {
			if !bare && isReferenceParam(root) {
				addDirect(SourceArgMutation, "arg-mutation "+root.Name)
			}
			return
		}
		if pkgVars[root.Name] {
			facts.assignedGlobals = append(facts.assignedGlobals, root.Name)
		}
	}

	var visit func(ast.Node) bool
	visit = func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				for _, lhs := range n.Lhs {
					noteAssignment(lhs)
				}
			}
		case *ast.IncDecStmt:
			noteAssignment(n.X)
		case *ast.CallExpr:
			noteCall(facts, n, fn, pkgPath, recvType, aliases, isLocal, addDirect)
		case *ast.SelectorExpr:
			ast.Inspect(n.X, visit)
			return false
		case *ast.Ident:
			if pkgVars[n.Name] && !isLocal(n) {
				facts.readGlobals = append(facts.readGlobals, n.Name)
			}
		}
		return true
	}
	ast.Inspect(fn.Body, visit)
	return facts
}

// noteCall records what a call expression reaches.
func noteCall(facts *funcFacts, call *ast.CallExpr, fn *ast.FuncDecl, pkgPath, recvType string, aliases map[string]string, isLocal func(*ast.Ident) bool, addDirect func(Source, string)) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if !isLocal(fun) {
			facts.callIDs = append(facts.callIDs, pkgPath+"."+fun.Name)
		}
	case *ast.SelectorExpr:
		x, ok := fun.X.(*ast.Ident)
		if !ok {
			return
		}
		if importPath, imported := aliases[x.Name]; imported && x.Obj == nil {
			facts.qcalls = append(facts.qcalls, qualifiedCall{importPath, fun.Sel.Name})
			if src, effectful := externalSource(importPath, fun.Sel.Name); effectful {
				addDirect(src, importPath+"."+fun.Sel.Name)
			}
			return
		}
		if recvType != "" && fn.Recv != nil && x.Obj != nil && x.Obj.Decl == fn.Recv.List[0] {
			facts.callIDs = append(facts.callIDs, pkgPath+"."+recvType+"."+fun.Sel.Name)
		}
	}
}
