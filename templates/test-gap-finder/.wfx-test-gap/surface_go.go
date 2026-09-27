//go:build ignore

// surface_go.go — the Go half of surface.py (`go run surface_go.go <files>`).
// Standard library only. Prints {file: [items]} as JSON: exported funcs,
// methods and types; if/else, switch cases, returned errors, panics, loops,
// nil comparisons and comparisons against literals, goroutines and channel ops.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

type item struct {
	Kind   string `json:"kind"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
	Detail string `json:"detail"`
}

func main() {
	out := map[string][]item{}
	fset := token.NewFileSet()
	for _, file := range os.Args[1:] {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			out[file] = []item{{"unparsable", 1, file, err.Error()}}
			continue
		}
		text := func(n ast.Node) string {
			s := string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[:i]
			}
			if len(s) > 160 {
				s = s[:160]
			}
			return s
		}
		line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }
		var items []item
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() {
						items = append(items, item{"class", line(ts), ts.Name.Name, "type " + ts.Name.Name + " — zero value, construction"})
					}
				}
			case *ast.FuncDecl:
				if !d.Name.IsExported() && d.Name.Name != "init" {
					continue
				}
				name := d.Name.Name
				kind := "function"
				if d.Recv != nil && len(d.Recv.List) > 0 {
					kind = "method"
					name = strings.TrimLeft(text(d.Recv.List[0].Type), "*") + "." + name
				}
				items = append(items, item{kind, line(d), name, text(d.Type) + " — every parameter: nil, empty, zero/negative, huge, unicode"})
				if d.Doc != nil {
					items = append(items, item{"doc_claim", line(d.Doc), name, strings.TrimSpace(d.Doc.Text())})
				}
				if d.Body == nil {
					continue
				}
				ast.Inspect(d.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.IfStmt:
						items = append(items, item{"branch", line(n), name, "if " + text(n.Cond)})
						if n.Else != nil {
							if _, chained := n.Else.(*ast.IfStmt); !chained {
								items = append(items, item{"branch", line(n.Else), name, "else of if " + text(n.Cond)})
							}
						}
					case *ast.CaseClause:
						items = append(items, item{"branch", line(n), name, text(n)})
					case *ast.ReturnStmt:
						for _, r := range n.Results {
							if c, ok := r.(*ast.CallExpr); ok && strings.Contains(text(c.Fun), "Errorf") || strings.Contains(text(r), "errors.New") || strings.Contains(text(r), "err") {
								items = append(items, item{"raise", line(n), name, text(n)})
								break
							}
						}
					case *ast.CallExpr:
						if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "panic" {
							items = append(items, item{"raise", line(n), name, text(n)})
						}
					case *ast.ForStmt, *ast.RangeStmt:
						items = append(items, item{"loop", line(n), name, text(n) + " — zero, one and many iterations"})
					case *ast.BinaryExpr:
						if n.Op == token.EQL || n.Op == token.NEQ || n.Op == token.LSS || n.Op == token.LEQ || n.Op == token.GTR || n.Op == token.GEQ {
							_, l := n.X.(*ast.BasicLit)
							_, r := n.Y.(*ast.BasicLit)
							nilCmp := text(n.Y) == "nil" || text(n.X) == "nil"
							if l || r || nilCmp {
								items = append(items, item{"boundary", line(n), name, text(n) + " — at, just below/above, nil"})
							}
						}
					case *ast.GoStmt:
						items = append(items, item{"concurrency", line(n), name, text(n) + " — races, ordering, leaks"})
					case *ast.SendStmt:
						items = append(items, item{"concurrency", line(n), name, text(n) + " — blocked or closed channel"})
					}
					return true
				})
			}
		}
		out[file] = items
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
