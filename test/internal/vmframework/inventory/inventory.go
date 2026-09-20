// Package inventory audits the bounded VM harness source surface, including
// build-tagged tests that the default Go package graph would omit.
package inventory

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
)

type Function struct {
	Name   string `json:"name"`
	Line   int    `json:"line"`
	End    int    `json:"end"`
	Tokens int    `json:"tokens"`
}

type File struct {
	Path      string     `json:"path"`
	SHA256    string     `json:"sha256"`
	Functions []Function `json:"functions,omitempty"`
}

type Group struct {
	Kind    string   `json:"kind"`
	Trivial bool     `json:"trivial"`
	Members []string `json:"members"`
}

type Report struct {
	Files      []File  `json:"files"`
	Duplicates []Group `json:"duplicate_candidates"`
}

// Scan includes every Go source/test file in the two adapters, all their smoke
// workflows, and both emergency cleanup scripts and script tests. Structural
// matches normalize identifiers/literals: they are review candidates, not proof
// that the functions have interchangeable semantics. Small matches are retained
// but marked trivial rather than silently omitted.
func Scan(root string) (Report, error) {
	var report Report
	groups := map[string][]string{}
	sizes := map[string]int{}
	patterns := []string{"test/hadron/*.go", "test/talos/*.go", ".github/workflows/hadron*.yml",
		".github/workflows/talos*.yml", "hack/hadron-cleanup.py", "hack/talos-cleanup.py",
		"hack/test_hadron_cleanup.py", "hack/test_talos_cleanup.py", "hack/hadron-vm-probe.sh",
		"hack/webhook-cert.sh", "Makefile"}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return report, err
		}
		if len(paths) == 0 {
			return report, fmt.Errorf("inventory source pattern matched nothing: %s", pattern)
		}
		for _, path := range paths {
			file, err := scanFile(root, path, groups, sizes)
			if err != nil {
				return report, err
			}
			report.Files = append(report.Files, file)
		}
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Path < report.Files[j].Path })
	for key, members := range groups {
		if len(members) > 1 {
			sort.Strings(members)
			report.Duplicates = append(report.Duplicates, Group{Kind: key[:5], Trivial: sizes[key] < 25, Members: members})
		}
	}
	sort.Slice(report.Duplicates, func(i, j int) bool {
		a, b := report.Duplicates[i], report.Duplicates[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Members[0] < b.Members[0]
	})
	return report, nil
}

func scanFile(root, path string, groups map[string][]string, sizes map[string]int) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return File{}, err
	}
	file := File{Path: filepath.ToSlash(relative), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	if filepath.Ext(path) != ".go" {
		return file, nil
	}
	fset := token.NewFileSet()
	tree, err := parser.ParseFile(fset, path, data, 0)
	if err != nil {
		return file, err
	}
	for _, decl := range tree.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var body bytes.Buffer
		if err := format.Node(&body, fset, fn.Body); err != nil {
			return file, err
		}
		exact, shape, count := fingerprints(body.Bytes())
		item := Function{Name: fn.Name.Name, Line: fset.Position(fn.Pos()).Line, End: fset.Position(fn.End()).Line, Tokens: count}
		file.Functions = append(file.Functions, item)
		for _, key := range []string{"exact:" + exact, "shape:" + shape} {
			groups[key] = append(groups[key], fmt.Sprintf("%s:%d:%s", file.Path, item.Line, item.Name))
			sizes[key] = count
		}
	}
	return file, nil
}

func fingerprints(body []byte) (string, string, int) {
	var scan scanner.Scanner
	scan.Init(token.NewFileSet().AddFile("body", -1, len(body)), body, nil, 0)
	var exact, shape bytes.Buffer
	count := 0
	for {
		_, tok, literal := scan.Scan()
		if tok == token.EOF {
			break
		}
		count++
		fmt.Fprintf(&exact, "%d:%q;", tok, literal)
		fmt.Fprintf(&shape, "%d;", tok)
	}
	return fmt.Sprintf("%x", sha256.Sum256(exact.Bytes())), fmt.Sprintf("%x", sha256.Sum256(shape.Bytes())), count
}
