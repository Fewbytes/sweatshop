package symbol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
)

// NewGo returns a Resolver for Go source files. Parsed files are cached for the
// lifetime of the resolver, since one scan asks about many lines per file.
func NewGo() Resolver { return &goResolver{cache: map[string]*parsedFile{}} }

type parsedFile struct {
	fset *token.FileSet
	file *ast.File
}

type goResolver struct {
	mu    sync.Mutex
	cache map[string]*parsedFile
}

func (g *goResolver) parse(path string) *parsedFile {
	g.mu.Lock()
	defer g.mu.Unlock()
	if pf, ok := g.cache[path]; ok {
		return pf
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	pf := (*parsedFile)(nil)
	if err == nil {
		pf = &parsedFile{fset: fset, file: f}
	}
	g.cache[path] = pf
	return pf
}

func (g *goResolver) Enclosing(path string, line int) (string, bool) {
	pf := g.parse(path)
	if pf == nil {
		return "", false
	}
	for _, decl := range pf.file.Decls {
		start := pf.fset.Position(decl.Pos()).Line
		end := pf.fset.Position(decl.End()).Line
		if line < start || line > end {
			continue
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			return funcName(d), true
		case *ast.GenDecl:
			if name, ok := genDeclName(pf, d, line); ok {
				return name, true
			}
		}
	}
	return "", false
}

// funcName renders a function as "Name" and a method as "Receiver.Name",
// ignoring pointer-ness so *T and T methods share a namespace.
func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return receiverTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver: T[P]
		return receiverTypeName(t.X)
	case *ast.IndexListExpr: // generic receiver: T[P, Q]
		return receiverTypeName(t.X)
	default:
		return "?"
	}
}

// genDeclName finds the specific const/var/type spec covering line, so a large
// var block does not collapse every finding inside it onto one name.
func genDeclName(pf *parsedFile, d *ast.GenDecl, line int) (string, bool) {
	for _, spec := range d.Specs {
		start := pf.fset.Position(spec.Pos()).Line
		end := pf.fset.Position(spec.End()).Line
		if line < start || line > end {
			continue
		}
		switch s := spec.(type) {
		case *ast.TypeSpec:
			return s.Name.Name, true
		case *ast.ValueSpec:
			if len(s.Names) > 0 {
				return s.Names[0].Name, true
			}
		}
	}
	return "", false
}
