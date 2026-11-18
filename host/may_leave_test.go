package host_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/core"
	"github.com/kjanat/wacogo/wasmtools"
)

// TestMayLeave_HostImport checks that ordinary guest calls can reach a
// lowered import, while realloc and post-return hooks cannot.
func TestMayLeave_HostImport(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	calls := 0
	hb := e.NewHostBuilder("provider-host")
	hb.AddFunction("f", &host.FuncType{}, func(context.Context, *core.CallContext, *host.ComponentInstance, []uint64) error {
		calls++
		return nil
	})
	hComp, err := hb.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hComp.Close(ctx)
	hInst, err := hComp.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hInst.Close(ctx)

	tool, err := wasmtools.Default(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := tool.Parse(ctx, []byte(`(component
  (import "f" (func $f))
  (core func $lowered (canon lower (func $f)))
  (core instance $env (export "f" (func $lowered)))
  (core module $m
    (import "env" "f" (func $f))
    (memory (export "m") 1)
    (func (export "r") (param i32 i32 i32 i32) (result i32) (call $f) i32.const 1024)
    (func (export "outside") (call $f))
    (func (export "finish") (call $f))
    (func (export "noop"))
    (func (export "lowered-into") (param i32 i32)))
  (core instance $i (instantiate $m (with "env" (instance $env))))
  (func (export "outside") (canon lift (core func $i "outside")))
  (func (export "post-return") (canon lift (core func $i "noop") (post-return (func $i "finish"))))
  (func (export "lowered-into") (param "s" string)
    (canon lift (core func $i "lowered-into") (memory $i "m") (realloc (func $i "r")))))`))
	if err != nil {
		t.Fatal(err)
	}
	comp, err := e.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	defer comp.Close(ctx)

	for _, tc := range []struct {
		name string
		args []core.Val
	}{
		{name: "lowered-into", args: []core.Val{core.ValString("hello")}},
		{name: "post-return"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := comp.Instantiate(ctx, wacogo.WithFuncImport("f", hInst.Core().ExportedFunc("f")))
			if err != nil {
				t.Fatal(err)
			}
			defer inst.Close(ctx)
			before := calls
			if _, err := inst.ExportedFunc("outside").Call(ctx); err != nil {
				t.Fatalf("outside: %v", err)
			}
			if calls != before+1 {
				t.Fatalf("outside: host function called %d times, want 1", calls-before)
			}
			_, err = inst.ExportedFunc(tc.name).Call(ctx, tc.args...)
			if err == nil || !strings.Contains(err.Error(), "cannot leave component instance") {
				t.Fatalf("got %v, want cannot leave component instance", err)
			}
			if calls != before+1 {
				t.Fatal("hook reached the host function")
			}
		})
	}
}
