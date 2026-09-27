// Command yaft-shell generates an empty shell for an interface: a type whose
// methods all return nothing. It is what yaft.Choose takes as the fallback of
// a class toggle that has none (R17), because Go cannot build an
// implementation of an interface at runtime.
//
// Use it with go generate, in the package that declares the interface:
//
//	//go:generate go run github.com/tehw0lf/yaft-go/cmd/yaft-shell -type Checkout
//
// This writes checkout_yaftshell.go with a NoopCheckout type and a
// NewNoopCheckout constructor returning it as a Checkout:
//
//	checkout := yaft.Choose("newCheckout", NewCheckout, NewNoopCheckout)
//
// "Nothing" is each result's zero value, except that a channel the caller can
// receive from comes back closed, so a receive returns instead of blocking --
// the same rule yaft.Func follows.
//
// Only what can be generated correctly is: an interface embedding another
// interface from a different package, or a generic interface, is refused.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	typeName := flag.String("type", "", "the interface to generate a shell for (required)")
	output := flag.String("output", "", "output file (default <type>_yaftshell.go)")
	dir := flag.String("dir", ".", "package directory")
	flag.Parse()

	if *typeName == "" {
		fmt.Fprintln(os.Stderr, "yaft-shell: -type is required")
		os.Exit(2)
	}
	out := *output
	if out == "" {
		out = filepath.Join(*dir, strings.ToLower(*typeName)+"_yaftshell.go")
	}

	src, err := generate(*dir, *typeName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yaft-shell:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "yaft-shell:", err)
		os.Exit(1)
	}
}

// method is one method of the interface, with its signature printed.
type method struct {
	name    string
	params  []string
	results []result
}

type result struct {
	typ      string
	chanElem string // set when the result is a channel the caller can receive from
	isChan   bool
}

// generate returns the formatted source of the shell for typeName in dir.
func generate(dir, typeName string) ([]byte, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	g := &generator{fset: fset, imports: map[string]string{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_yaftshell.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if g.pkgName == "" {
			g.pkgName = file.Name.Name
		} else if g.pkgName != file.Name.Name {
			return nil, fmt.Errorf("%s holds packages %s and %s", dir, g.pkgName, file.Name.Name)
		}
		g.files = append(g.files, file)
	}
	if len(g.files) == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}

	methods, err := g.methods(typeName, map[string]bool{})
	if err != nil {
		return nil, err
	}
	return g.render(g.pkgName, typeName, methods)
}

type generator struct {
	fset    *token.FileSet
	pkgName string
	files   []*ast.File
	imports map[string]string // package name used in signatures -> import line
}

// lookup finds the interface declaration and the file it lives in.
func (g *generator) lookup(name string) (*ast.TypeSpec, *ast.File, error) {
	for _, file := range g.files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if ts := spec.(*ast.TypeSpec); ts.Name.Name == name {
					return ts, file, nil
				}
			}
		}
	}
	return nil, nil, fmt.Errorf("type %s not found in package %s", name, g.pkgName)
}

func (g *generator) methods(name string, seen map[string]bool) ([]method, error) {
	if seen[name] {
		return nil, nil
	}
	seen[name] = true

	spec, file, err := g.lookup(name)
	if err != nil {
		return nil, err
	}
	iface, ok := spec.Type.(*ast.InterfaceType)
	if !ok {
		return nil, fmt.Errorf("%s is not an interface", name)
	}
	if spec.TypeParams != nil {
		return nil, fmt.Errorf("%s is generic; generic interfaces are not supported", name)
	}

	var methods []method
	for _, field := range iface.Methods.List {
		switch t := field.Type.(type) {
		case *ast.FuncType:
			for _, n := range field.Names {
				m, err := g.method(n.Name, t, file)
				if err != nil {
					return nil, err
				}
				methods = append(methods, m)
			}
		case *ast.Ident:
			embedded, err := g.methods(t.Name, seen)
			if err != nil {
				return nil, fmt.Errorf("embedded in %s: %w", name, err)
			}
			methods = append(methods, embedded...)
		default:
			return nil, fmt.Errorf("%s embeds %s; only interfaces from the same package can be embedded",
				name, g.print(field.Type))
		}
	}
	return methods, nil
}

func (g *generator) method(name string, fn *ast.FuncType, file *ast.File) (method, error) {
	if err := g.collectImports(fn, file); err != nil {
		return method{}, err
	}
	m := method{name: name}
	for _, p := range fields(fn.Params) {
		m.params = append(m.params, g.print(p))
	}
	for _, r := range fields(fn.Results) {
		res := result{typ: g.print(r)}
		if ch, ok := r.(*ast.ChanType); ok && ch.Dir&ast.RECV != 0 {
			res.isChan, res.chanElem = true, g.print(ch.Value)
		}
		m.results = append(m.results, res)
	}
	return m, nil
}

// fields expands "a, b int" into one type per name.
func fields(list *ast.FieldList) []ast.Expr {
	if list == nil {
		return nil
	}
	var types []ast.Expr
	for _, f := range list.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			types = append(types, f.Type)
		}
	}
	return types
}

// collectImports records the import of every package the signature names, as
// the declaring file spells it.
func (g *generator) collectImports(fn *ast.FuncType, file *ast.File) error {
	var err error
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		line, found := importFor(file, pkgIdent.Name)
		if !found {
			err = fmt.Errorf("cannot find the import for %s.%s", pkgIdent.Name, sel.Sel.Name)
			return false
		}
		g.imports[pkgIdent.Name] = line
		return true
	})
	return err
}

func importFor(file *ast.File, name string) (string, bool) {
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if imp.Name != nil {
			if imp.Name.Name == name {
				return imp.Name.Name + " " + imp.Path.Value, true
			}
			continue
		}
		// The package name is conventionally the last path element; a
		// "go-" prefix or ".v2" suffix would need an explicit alias.
		base := path[strings.LastIndex(path, "/")+1:]
		if base == name {
			return imp.Path.Value, true
		}
	}
	return "", false
}

func (g *generator) print(node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, g.fset, node); err != nil {
		panic(err) // printing a node the parser produced cannot fail
	}
	return buf.String()
}

func (g *generator) render(pkgName, typeName string, methods []method) ([]byte, error) {
	shell := "Noop" + upperFirst(typeName)
	ctor := "New" + shell
	if !ast.IsExported(typeName) {
		shell, ctor = "noop"+upperFirst(typeName), "newNoop"+upperFirst(typeName)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by yaft-shell; DO NOT EDIT.\n\npackage %s\n\n", pkgName)
	if len(g.imports) > 0 {
		lines := make([]string, 0, len(g.imports))
		for _, line := range g.imports {
			lines = append(lines, line)
		}
		sort.Strings(lines)
		b.WriteString("import (\n")
		for _, line := range lines {
			b.WriteString("\t" + line + "\n")
		}
		b.WriteString(")\n\n")
	}
	fmt.Fprintf(&b, "// %s is the empty shell for %s: every method returns nothing. Pass %s as\n", shell, typeName, ctor)
	fmt.Fprintf(&b, "// the fallback of yaft.Choose for a class toggle without one of its own.\n")
	fmt.Fprintf(&b, "type %s struct{}\n\n", shell)
	fmt.Fprintf(&b, "// %s returns the empty shell as a %s.\n", ctor, typeName)
	fmt.Fprintf(&b, "func %s() %s { return %s{} }\n", ctor, typeName, shell)

	names := map[string]bool{}
	for _, m := range methods {
		if names[m.name] {
			return nil, fmt.Errorf("method %s is declared twice", m.name)
		}
		names[m.name] = true

		params := make([]string, len(m.params))
		for i, p := range m.params {
			params[i] = "_ " + p
		}
		results := make([]string, len(m.results))
		for i, r := range m.results {
			results[i] = fmt.Sprintf("r%d %s", i, r.typ)
		}
		fmt.Fprintf(&b, "\n// %s returns nothing.\n", m.name)
		fmt.Fprintf(&b, "func (%s) %s(%s)", shell, m.name, strings.Join(params, ", "))
		if len(results) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(results, ", "))
		}
		b.WriteString(" {\n")
		for i, r := range m.results {
			if r.isChan {
				fmt.Fprintf(&b, "\tc%d := make(chan %s)\n\tclose(c%d)\n\tr%d = c%d\n", i, r.chanElem, i, i, i)
			}
		}
		b.WriteString("\treturn\n}\n")
	}

	src, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, errors.Join(errors.New("generated code does not format"), err)
	}
	return src, nil
}

func upperFirst(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}
