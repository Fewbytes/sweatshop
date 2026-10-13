package detector

import (
	"context"
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// Context closure (research-docs/llm-code-quality/README.md section 5, metric
// 1): tokens in a function plus the transitive package-level definitions it
// references. Definitions:
//   - tokens are estimated as ceil(bytes / closureBytesPerToken) of the source
//     text, comments included, so the number needs no tokenizer;
//   - the closure follows identifiers that name a function, type, constant or
//     variable declared in the same package (same directory, non-test files),
//     transitively, counting each definition once. Methods are roots but are
//     not followed, because resolving them needs type information;
//   - identifiers are matched by name without scope analysis, so a local
//     variable shadowing a package-level name overcounts.
//
// A function whose closure exceeds closureMaxTokens is reported.
const (
	closureBytesPerToken = 4
	closureMaxTokens     = 4000
)

// NewContextClosure reports functions whose context closure is too large to
// read in one pass.
func NewContextClosure(sym symbol.Resolver) Detector { return &contextClosure{sym: sym} }

type contextClosure struct{ sym symbol.Resolver }

func (*contextClosure) Name() string { return "contextclosure" }

func (*contextClosure) Available(context.Context) bool { return true }

// definition is a package-level declaration that can be pulled into a closure.
type definition struct {
	Tokens int
	Refs   []string
}

func (c *contextClosure) Run(ctx context.Context, dir string, _ []string) ([]finding.Hit, error) {
	tree, err := loadGoTree(dir)
	if err != nil {
		return nil, err
	}
	byDir := map[string][]goSource{}
	for _, f := range tree.Files {
		if !f.IsTest {
			byDir[f.Dir] = append(byDir[f.Dir], f)
		}
	}

	var hits []finding.Hit
	for _, files := range byDir {
		defs := packageDefinitions(files)
		for _, f := range files {
			for _, decl := range f.File.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				root := nodeDefinition(f, fn)
				selfName := ""
				if fn.Recv == nil {
					selfName = fn.Name.Name
				}
				total, count := closureTokens(root, selfName, defs)
				if total <= closureMaxTokens {
					continue
				}
				line := f.Fset.Position(fn.Pos()).Line
				hits = append(hits, finding.Hit{
					RuleID: "contextclosure/too-large",
					File:   f.Path,
					Line:   line,
					Symbol: symbol.Resolve(c.sym, filepath.Join(dir, f.Path), line),
					Message: fmt.Sprintf("%s needs about %d tokens of context (%d itself, plus %d definitions); limit %d",
						fn.Name.Name, total, root.Tokens, count, closureMaxTokens),
					Severity:  finding.SeverityLow,
					MatchText: fn.Name.Name,
				})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].File != hits[j].File {
			return hits[i].File < hits[j].File
		}
		return hits[i].Line < hits[j].Line
	})
	return hits, nil
}

// packageDefinitions indexes the package-level funcs, types, consts and vars
// of one package by name. Methods are excluded.
func packageDefinitions(files []goSource) map[string]definition {
	defs := map[string]definition{}
	for _, f := range files {
		for _, decl := range f.File.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					defs[d.Name.Name] = nodeDefinition(f, d)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						defs[s.Name.Name] = nodeDefinition(f, s)
					case *ast.ValueSpec:
						def := nodeDefinition(f, s)
						for _, name := range s.Names {
							defs[name.Name] = def
						}
					}
				}
			}
		}
	}
	return defs
}

func nodeDefinition(f goSource, n ast.Node) definition {
	size := f.Fset.Position(n.End()).Offset - f.Fset.Position(n.Pos()).Offset
	return definition{
		Tokens: (size + closureBytesPerToken - 1) / closureBytesPerToken,
		Refs:   referencedNames(n),
	}
}

// referencedNames lists the identifiers in n that could name a package-level
// definition. The Sel of x.Sel and the names of fields and parameters are
// skipped: they name members or locals, not package-level symbols.
func referencedNames(root ast.Node) []string {
	seen := map[string]bool{}
	var names []string
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			ast.Inspect(v.X, visit)
			return false
		case *ast.Field:
			ast.Inspect(v.Type, visit)
			return false
		case *ast.Ident:
			if !seen[v.Name] {
				seen[v.Name] = true
				names = append(names, v.Name)
			}
		}
		return true
	}
	ast.Inspect(root, visit)
	return names
}

// closureTokens sums root and every definition reachable from it, each once.
// rootName is the root's own package-level name, or "" for a method, so a
// recursive function is not counted twice. It returns the token total and the
// number of definitions pulled in.
func closureTokens(root definition, rootName string, defs map[string]definition) (tokens, pulledIn int) {
	tokens = root.Tokens
	visited := map[string]bool{rootName: rootName != ""}
	queue := append([]string(nil), root.Refs...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		def, ok := defs[name]
		if !ok || visited[name] {
			continue
		}
		visited[name] = true
		tokens += def.Tokens
		pulledIn++
		queue = append(queue, def.Refs...)
	}
	return tokens, pulledIn
}
