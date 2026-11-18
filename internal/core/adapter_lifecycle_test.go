package core

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/kjanat/wacogo/internal/canon"
	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
)

func TestHostFuncs_OneModulePerInstance(t *testing.T) {
	ctx := context.Background()
	engine, rt := trackAdapterCode(t)
	component, opts := loadManyHostImports(t, engine, 32)
	inst, err := component.Instantiate(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if rt.hostBuilds != 1 {
		t.Fatalf("built %d host modules for 32 imports, want 1", rt.hostBuilds)
	}
	if err := inst.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.liveHost != 0 {
		t.Fatalf("%d live host module handles after close, want 0", rt.liveHost)
	}
}

func TestAdapterModules_ReleaseAfterClose(t *testing.T) {
	const wat = `(component
		(component $provider
			(core module $m
				(memory (export "mem") 1)
				(func (export "realloc%d") (param i32 i32 i32 i32) (result i32) i32.const 1024)
				(func (export "sink") (param i32 i32)))
			(core instance $i (instantiate $m))
			(func (export "sink") (param "s" string) (canon lift (core func $i "sink")
				(memory $i "mem") (realloc (func $i "realloc%d")))))
		(instance $p (instantiate $provider))
		(alias export $p "sink" (func $sink))
		(core module $mem (memory (export "m") 1) (data (i32.const 4) "abc"))
		(core instance $mem (instantiate $mem))
		(core func $lower (canon lower (func $sink) (memory $mem "m")))
		(core instance $env (export "f%d" (func $lower)))
		(core module $m
			(import "env" "f%d" (func $f (param i32 i32)))
			(func (export "run") i32.const 4 i32.const 3 call $f))
		(core instance $i (instantiate $m (with "env" (instance $env))))
		(func (export "run") (canon lift (core func $i "run"))))`

	ctx := context.Background()
	engine, rt := trackAdapterCode(t)
	for i := range 32 {
		comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, fmt.Sprintf(wat, i, i, i, i))))
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		inst, err := comp.Instantiate(ctx)
		if err != nil {
			t.Fatalf("instantiate %d: %v", i, err)
		}
		if _, err := inst.ExportedFunc("run").Call(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if err := inst.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := comp.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if rt.liveWasm != 1 || rt.liveHost != 0 {
			t.Fatalf("cycle %d: %d live Wasm and %d host handles; want only the shared realloc helper", i, rt.liveWasm, rt.liveHost)
		}
	}
	if err := engine.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.liveWasm != 0 || rt.liveHost != 0 {
		t.Fatalf("closed engine retained %d Wasm and %d host handles", rt.liveWasm, rt.liveHost)
	}
}

// Track compiled handles through the public wazero interface, including when
// the caller owns a compilation cache that outlives this runtime.
func trackAdapterCode(t *testing.T) (*Engine, *adapterCodeRuntime) {
	t.Helper()
	ctx := context.Background()
	cache := wazero.NewCompilationCache()
	t.Cleanup(func() { _ = cache.Close(ctx) })
	engine := NewEngine(ctx, WithRuntimeConfig(wazero.NewRuntimeConfig().WithCompilationCache(cache)))
	t.Cleanup(func() { _ = engine.Close(ctx) })
	rt := &adapterCodeRuntime{Runtime: engine.runtime}
	engine.runtime = rt
	engine.canonHost = canon.NewHost(rt)
	return engine, rt
}

type adapterCodeRuntime struct {
	wazero.Runtime
	liveWasm, liveHost, hostBuilds int
}

func (r *adapterCodeRuntime) CompileModule(ctx context.Context, binary []byte) (wazero.CompiledModule, error) {
	code, err := r.Runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, err
	}
	r.liveWasm++
	return &trackedAdapterCode{CompiledModule: code, live: &r.liveWasm}, nil
}

func (r *adapterCodeRuntime) InstantiateModule(ctx context.Context, code wazero.CompiledModule, cfg wazero.ModuleConfig) (api.Module, error) {
	if tracked, ok := code.(*trackedAdapterCode); ok {
		code = tracked.CompiledModule
	}
	return r.Runtime.InstantiateModule(ctx, code, cfg)
}

func (r *adapterCodeRuntime) NewHostModuleBuilder(name string) wazero.HostModuleBuilder {
	r.hostBuilds++
	return &adapterCodeBuilder{HostModuleBuilder: r.Runtime.NewHostModuleBuilder(name), rt: r}
}

type adapterCodeBuilder struct {
	wazero.HostModuleBuilder
	rt *adapterCodeRuntime
}

func (b *adapterCodeBuilder) Compile(ctx context.Context) (wazero.CompiledModule, error) {
	code, err := b.HostModuleBuilder.Compile(ctx)
	if err != nil {
		return nil, err
	}
	b.rt.liveHost++
	return &trackedAdapterCode{CompiledModule: code, live: &b.rt.liveHost}, nil
}

type trackedAdapterCode struct {
	wazero.CompiledModule
	live *int
}

func (c *trackedAdapterCode) Close(ctx context.Context) error {
	err := c.CompiledModule.Close(ctx)
	if err == nil && c.live != nil {
		(*c.live)--
		c.live = nil
	}
	return err
}
