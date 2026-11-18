package core

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kjanat/wacogo/wasmtools"
)

func loadManyHostImports(tb testing.TB, engine *Engine, n int) (*Component, []InstantiateOption) {
	tb.Helper()
	ctx := context.Background()
	tool, err := wasmtools.Default(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	load := func(wat string) *Component {
		bin, err := tool.Parse(ctx, []byte(wat))
		if err != nil {
			tb.Fatal(err)
		}
		comp, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = comp.Close(ctx) })
		return comp
	}
	provider := load(`(component
		(core module $m (func (export "noop")))
		(core instance $i (instantiate $m))
		(func (export "noop") (canon lift (core func $i "noop"))))`)
	host, err := provider.Instantiate(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = host.Close(ctx) })
	noop := host.ExportedFunc("noop")

	var wat strings.Builder
	wat.WriteString("(component")
	opts := make([]InstantiateOption, 0, n)
	for i := range n {
		fmt.Fprintf(&wat, ` (import "f%d" (func $f%d)) (core func $l%d (canon lower (func $f%d)))`, i, i, i, i)
		opts = append(opts, WithFuncImport(fmt.Sprintf("f%d", i), noop))
	}
	wat.WriteString(" (core instance $env")
	for i := range n {
		fmt.Fprintf(&wat, ` (export "f%d" (func $l%d))`, i, i)
	}
	wat.WriteString(") (core module $m")
	for i := range n {
		fmt.Fprintf(&wat, ` (import "env" "f%d" (func))`, i)
	}
	wat.WriteString(") (core instance (instantiate $m (with \"env\" (instance $env)))))")
	return load(wat.String()), opts
}

func BenchmarkInstantiateWithHostImports(b *testing.B) {
	for _, n := range []int{4, 128} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			engine := NewEngine(ctx)
			b.Cleanup(func() { _ = engine.Close(ctx) })
			component, opts := loadManyHostImports(b, engine, n)
			b.ReportAllocs()
			for b.Loop() {
				inst, err := component.Instantiate(ctx, opts...)
				if err != nil {
					b.Fatal(err)
				}
				if err := inst.Close(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
