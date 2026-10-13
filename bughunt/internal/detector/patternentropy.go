package detector

import (
	"context"
	"fmt"
	"go/ast"
	"math"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Fewbytes/sweatshop/bughunt/internal/finding"
	"github.com/Fewbytes/sweatshop/bughunt/internal/symbol"
)

// Pattern entropy (research-docs/llm-code-quality/README.md section 5,
// metric 6): distinct idioms used for the same concern. Each concern has a
// closed catalogue of idioms (callIdioms plus the two special cases in
// classifyCall); non-test Go files are scanned and calls are matched on the
// resolved import path, so aliases and shadowing by local names do not matter.
// A concern is reported when at least patternEntropyMinIdioms idioms are in use;
// every idiom except the most used one gets a hit anchored at its first site.

const patternEntropyMinIdioms = 2

type idiomKey struct{ Concern, Idiom string }

// callIdioms maps "<import path>.<function>" to the idiom it implements.
var callIdioms = map[string]idiomKey{
	"log.Print":      {"logging", "log"},
	"log.Printf":     {"logging", "log"},
	"log.Println":    {"logging", "log"},
	"log.Fatal":      {"logging", "log"},
	"log.Fatalf":     {"logging", "log"},
	"log.Fatalln":    {"logging", "log"},
	"log/slog.Debug": {"logging", "log/slog"},
	"log/slog.Info":  {"logging", "log/slog"},
	"log/slog.Warn":  {"logging", "log/slog"},
	"log/slog.Error": {"logging", "log/slog"},

	"errors.New": {"leaf-error", "errors.New"},

	"os.ReadFile":        {"file-read", "os.ReadFile"},
	"io/ioutil.ReadFile": {"file-read", "io/ioutil.ReadFile"},
}

const (
	idiomStderrPrint = "fmt.Fprint*(os.Stderr)"
	idiomErrorfLeaf  = "fmt.Errorf"
)

// NewPatternEntropy reports concerns implemented with more than one idiom.
func NewPatternEntropy(sym symbol.Resolver) Detector { return &patternEntropy{sym: sym} }

type patternEntropy struct{ sym symbol.Resolver }

func (*patternEntropy) Name() string { return "patternentropy" }

func (*patternEntropy) Available(context.Context) bool { return true }

type idiomSite struct {
	File string
	Line int
	N    int
}

func (p *patternEntropy) Run(ctx context.Context, dir string, _ []string) ([]finding.Hit, error) {
	tree, err := loadGoTree(dir)
	if err != nil {
		return nil, err
	}
	sites := map[idiomKey]*idiomSite{}
	for _, src := range tree.Files {
		if src.IsTest {
			continue
		}
		imports := importNames(src.File)
		ast.Inspect(src.File, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key, ok := classifyCall(call, imports)
			if !ok {
				return true
			}
			if s := sites[key]; s != nil {
				s.N++
			} else {
				sites[key] = &idiomSite{File: src.Path, Line: src.Fset.Position(call.Pos()).Line, N: 1}
			}
			return true
		})
	}

	byConcern := map[string][]idiomKey{}
	for k := range sites {
		byConcern[k.Concern] = append(byConcern[k.Concern], k)
	}
	concerns := make([]string, 0, len(byConcern))
	for c := range byConcern {
		concerns = append(concerns, c)
	}
	sort.Strings(concerns)

	var hits []finding.Hit
	for _, concern := range concerns {
		keys := byConcern[concern]
		if len(keys) < patternEntropyMinIdioms {
			continue
		}
		sort.Slice(keys, func(i, j int) bool {
			if ni, nj := sites[keys[i]].N, sites[keys[j]].N; ni != nj {
				return ni > nj
			}
			return keys[i].Idiom < keys[j].Idiom
		})
		dominant := keys[0]
		bits := shannonBits(keys, sites)
		for _, k := range keys[1:] {
			s := sites[k]
			abs := filepath.Join(dir, s.File)
			hits = append(hits, finding.Hit{
				RuleID: "patternentropy/" + concern,
				File:   s.File,
				Line:   s.Line,
				Symbol: symbol.Resolve(p.sym, abs, s.Line),
				Message: fmt.Sprintf("%s: %q used %d times but %q is dominant (%d times); %d idioms, %.2f bits",
					concern, k.Idiom, s.N, dominant.Idiom, sites[dominant].N, len(keys), bits),
				Severity:  finding.SeverityLow,
				MatchText: concern + "/" + k.Idiom,
			})
		}
	}
	return hits, nil
}

func shannonBits(keys []idiomKey, sites map[idiomKey]*idiomSite) float64 {
	total := 0
	for _, k := range keys {
		total += sites[k].N
	}
	bits := 0.0
	for _, k := range keys {
		pr := float64(sites[k].N) / float64(total)
		bits -= pr * math.Log2(pr)
	}
	return bits
}

// importNames maps each file-local package name to its import path.
func importNames(f *ast.File) map[string]string {
	names := map[string]string{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	return names
}

func classifyCall(call *ast.CallExpr, imports map[string]string) (idiomKey, bool) {
	pkg, fn, ok := qualifiedCall(call, imports)
	if !ok {
		return idiomKey{}, false
	}
	switch {
	case pkg == "fmt" && strings.HasPrefix(fn, "Fprint") && len(call.Args) > 0 && isOsStderr(call.Args[0], imports):
		return idiomKey{"logging", idiomStderrPrint}, true
	case pkg == "fmt" && fn == "Errorf" && !errorfWraps(call):
		return idiomKey{"leaf-error", idiomErrorfLeaf}, true
	}
	key, ok := callIdioms[pkg+"."+fn]
	return key, ok
}

// qualifiedCall resolves pkg.Func(...) to its import path and function name.
func qualifiedCall(call *ast.CallExpr, imports map[string]string) (pkg, fn string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isIdent := sel.X.(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	pkg, ok = imports[id.Name]
	return pkg, sel.Sel.Name, ok
}

func isOsStderr(e ast.Expr, imports map[string]string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Stderr" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && imports[id.Name] == "os"
}

// errorfWraps reports whether the format string is a literal containing %w.
func errorfWraps(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return false
	}
	format, err := strconv.Unquote(lit.Value)
	return err == nil && strings.Contains(format, "%w")
}
