package core

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The expected results are wasmtime 51.0.0's for the same files.
var closureConformance = map[string]struct {
	loadErr string
	results map[string]uint32
	traps   []string
}{
	"c2a_outer_alias_resource_rejected.wat":     {loadErr: "cannot alias outer type which transitively refers to resources not defined in the current component"},
	"c2d_fresh_resource_per_instance.wat":       {results: map[string]uint32{"f": 9}, traps: []string{"g"}},
	"c3a_through_B.wat":                         {results: map[string]uint32{"f": 2}},
	"c3b_two_hops.wat":                          {results: map[string]uint32{"f": 2}},
	"c3c_instantiated_in_B.wat":                 {results: map[string]uint32{"f": 2}},
	"c3d_import_placeholder_two_instances.wat":  {results: map[string]uint32{"f": 5, "g": 6}},
	"c3e_nested_in_C.wat":                       {results: map[string]uint32{"f": 2}},
	"c3f_inline_instance_export.wat":            {results: map[string]uint32{"f": 2}},
	"c4a_in_scope_and_after_leaving.wat":        {results: map[string]uint32{"f": 5, "g": 5, "h": 6, "k": 6}},
	"c4b_two_exits_same_component.wat":          {results: map[string]uint32{"f": 5, "g": 5, "h": 6, "k": 6}},
	"c4c_arg_inside_defining_scope.wat":         {results: map[string]uint32{"f": 5, "g": 6, "h": 6}},
	"c5a_record_depth1.wat":                     {results: map[string]uint32{"f": 42}},
	"c5b_record_depth2_decoy.wat":               {results: map[string]uint32{"f": 42}},
	"c5c_record_instance_type_depth2.wat":       {results: map[string]uint32{"f": 42}},
	"c5d_core_type_outer.wat":                   {results: map[string]uint32{"f": 7}},
	"c5e_functype_outer.wat":                    {results: map[string]uint32{"f": 9}},
	"c5f_type_closure_instantiated_deeper.wat":  {results: map[string]uint32{"f": 42}},
	"c5g_type_closure_instantiated_at_root.wat": {results: map[string]uint32{"f": 42}},
}

func TestOuterClosuresMatchWasmtime(t *testing.T) {
	files, err := filepath.Glob("testdata/closures/*.wat")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no closure test files")
	}
	for _, file := range files {
		name := filepath.Base(file)
		want, ok := closureConformance[name]
		if !ok && strings.HasPrefix(name, "c1_") {
			want.results = map[string]uint32{"f": 7}
			ok = true
		}
		if !ok {
			t.Errorf("%s has no expected result", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			wat, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			engine := NewEngine(ctx)
			defer engine.Close(ctx)
			comp, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, string(wat))))
			if want.loadErr != "" {
				if err == nil || !strings.Contains(err.Error(), want.loadErr) {
					t.Fatalf("load = %v, want error containing %q", err, want.loadErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer comp.Close(ctx)
			inst, err := comp.Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer inst.Close(ctx)
			for export, value := range want.results {
				got, err := inst.ExportedFunc(export).Call(ctx)
				if err != nil {
					t.Fatalf("%s(): %v", export, err)
				}
				if len(got) != 1 || got[0] != ValU32(value) {
					t.Fatalf("%s() = %v, want [%d]", export, got, value)
				}
			}
			for _, export := range want.traps {
				if got, err := inst.ExportedFunc(export).Call(ctx); err == nil {
					t.Fatalf("%s() = %v, want a trap", export, got)
				}
			}
		})
	}
	for name := range closureConformance {
		if !slices.Contains(files, filepath.Join("testdata/closures", name)) {
			t.Errorf("expected result for missing file %s", name)
		}
	}
}
