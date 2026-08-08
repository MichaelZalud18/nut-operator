/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"
)

// TestMainRegistersEachReconcilerOnce guards a failure mode the envtest suites structurally cannot
// catch: they wire reconcilers individually, so nothing exercised main.go's own wiring. A reconciler
// registered twice passes every test and then fails at runtime, because controller-runtime rejects
// the duplicate controller name and main exits non-zero — the manager crash-loops and the operator
// never starts at all. That is exactly what happened with UPSCapabilityProbeReconciler, caught only
// by deploying the bundled manifest to a live cluster.
//
// This reads the source rather than calling the wiring, because the wiring is inlined in main() and
// needs a real API server. A shape test is the cheap guard; it fails loudly on the one mistake that
// is both easy to make and invisible to the rest of the suite.
func TestMainRegistersEachReconcilerOnce(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	counts := map[string]int{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "SetupWithManager" {
			return true
		}
		if name := reconcilerTypeName(selector.X); name != "" {
			counts[name]++
		}
		return true
	})

	if len(counts) == 0 {
		t.Fatal("found no reconciler SetupWithManager calls in main.go; this test is no longer measuring anything")
	}

	duplicates := make([]string, 0)
	for name, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, name)
		}
	}
	sort.Strings(duplicates)
	if len(duplicates) > 0 {
		t.Fatalf("reconcilers registered more than once in main.go: %v — controller-runtime rejects duplicate controller names and the manager will crash-loop", duplicates)
	}
}

// reconcilerTypeName pulls the reconciler type out of the scaffolded
// `(&controller.XReconciler{...}).SetupWithManager(mgr)` shape.
func reconcilerTypeName(expr ast.Expr) string {
	if paren, ok := expr.(*ast.ParenExpr); ok {
		expr = paren.X
	}
	unary, ok := expr.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return ""
	}
	composite, ok := unary.X.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	switch typeExpr := composite.Type.(type) {
	case *ast.SelectorExpr:
		return typeExpr.Sel.Name
	case *ast.Ident:
		return typeExpr.Name
	}
	return ""
}
