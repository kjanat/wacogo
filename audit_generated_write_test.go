package wacogo_test

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/primlists"
)

func TestAuditGeneratedWriteFailure(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)
	wat := `(component
 (core module $m
 (memory (export "memory") 1)
 (global $calls (mut i32) (i32.const 0))
 (func (export "realloc") (param i32 i32 i32 i32) (result i32) unreachable)
 (func (export "run") (param i32 i32) (result i32) global.get $calls i32.const 1 i32.add global.set $calls i32.const 0)
 (func (export "calls") (result i32) global.get $calls))
 (core instance $i (instantiate $m))
 (func (export "rev-u8") (param "xs" (list u8)) (result (list u8)) (canon lift (core func $i "run") (memory (core memory $i "memory")) (realloc (core func $i "realloc"))))
 (func (export "calls") (result u32) (canon lift (core func $i "calls"))))`
	cmd := exec.Command("wasm-tools", "parse", "-")
	cmd.Stdin = strings.NewReader(wat)
	bin, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parse: %v %s", err, bin)
	}
	comp, err := e.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := comp.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close(ctx)
	hc, err := e.NewHostBuilder("caller").Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hc.Close(ctx)
	caller, err := hc.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close(ctx)
	_, err = primlists.WrapInstance(caller, inst).RevU8(ctx, []byte{1})
	if err == nil {
		t.Fatal("expected failing realloc")
	}
	t.Logf("generated_wrapper_error=%v", err)
	if p := inst.Poisoned(); p != nil {
		t.Logf("callee poisoned: %v", p)
		return
	}
	result, err := inst.ExportedFunc("calls").Call(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("guest_function_calls=%v", result)
	if len(result) != 1 || result[0] != wacogo.ValU32(0) {
		t.Fatal("actual generated wrapper called guest after argument realloc trapped")
	}
}
