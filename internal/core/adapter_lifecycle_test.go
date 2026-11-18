package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kjanat/wacogo/internal/canon"
	"github.com/kjanat/wacogo/internal/testutil"
	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
	"github.com/kjanat/wazero/experimental"
)

func TestHostFuncs_OneModulePerInstance(t *testing.T) {
	ctx := context.Background()
	engine, rt, _ := trackAdapterCode(t)
	component, opts := loadManyHostImports(t, engine, 32)
	inst, err := component.Instantiate(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if rt.HostBuilds.Load() != 1 {
		t.Fatalf("built %d host modules for 32 imports, want 1", rt.HostBuilds.Load())
	}
	if err := inst.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.LiveHost.Load() != 0 {
		t.Fatalf("%d live host module handles after close, want 0", rt.LiveHost.Load())
	}
}

const adapterLifecycleWat = `(component
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

func TestAdapterModules_ReleaseAfterClose(t *testing.T) {
	ctx := context.Background()
	engine, rt, _ := trackAdapterCode(t)
	for i := range 32 {
		comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, fmt.Sprintf(adapterLifecycleWat, i, i, i, i))))
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
		if rt.LiveWasm.Load() != 1 || rt.LiveHost.Load() != 0 {
			t.Fatalf("cycle %d: %d live Wasm and %d host handles; want only the shared realloc helper", i, rt.LiveWasm.Load(), rt.LiveHost.Load())
		}
	}
	if err := engine.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.LiveWasm.Load() != 0 || rt.LiveHost.Load() != 0 {
		t.Fatalf("closed engine retained %d Wasm and %d host handles", rt.LiveWasm.Load(), rt.LiveHost.Load())
	}
}

func TestHostFuncs_Rollback(t *testing.T) {
	for _, mode := range []string{"instantiate first chunk", "compile second chunk", "reject bound signature"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			engine, rt, cache := trackAdapterCode(t)
			fault := errors.New("injected host module failure")
			var comp *Component
			var opts []InstantiateOption
			var err error
			if mode == "reject bound signature" {
				comp, err = engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, fmt.Sprintf(adapterLifecycleWat, 0, 0, 0, 0))))
				if err != nil {
					t.Fatal(err)
				}
				// The helper and its bridge are built before bindHostFunc
				// rejects a signature that differs from the validated slot.
				comp.hostFuncs[0].params = nil
			} else {
				comp, opts = loadManyHostImports(t, engine, 1)
				if mode == "instantiate first chunk" {
					rt.BeforeInstantiate = func(_ context.Context, c *testutil.Code) error {
						if c.Host {
							return fault
						}
						return nil
					}
				} else {
					comp.hostFuncs = slices.Repeat(comp.hostFuncs, maxHostFuncsPerModule+1)
					rt.BeforeHostCompile = func() error {
						if rt.HostBuilds.Load() == 2 {
							return fault
						}
						return nil
					}
				}
			}
			modulesBefore, codeBefore := len(rt.Modules()), rt.LiveWasm.Load()
			inst, err := comp.Instantiate(ctx, opts...)
			if mode == "reject bound signature" {
				if err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("expected adapter signature rejection: %v", err)
				}
				codeBefore++ // The engine keeps the shared helper.
			} else if !errors.Is(err, fault) {
				t.Fatalf("expected injected host module failure: %v", err)
			}
			wantModules := 0
			switch mode {
			case "compile second chunk":
				wantModules = 1
			case "reject bound signature":
				wantModules = 5 // Host chunk, two guest modules, helper and bridge.
			}
			mods := rt.Modules()[modulesBefore:]
			if inst != nil || rt.LiveHost.Load() != 0 || rt.LiveWasm.Load() != codeBefore || len(mods) != wantModules {
				t.Fatalf("rollback left %d new modules, %d host handles and %d Wasm handles (expected %d modules and %d Wasm handles)",
					len(mods), rt.LiveHost.Load(), rt.LiveWasm.Load(), wantModules, codeBefore)
			}
			for _, mod := range mods {
				if !mod.IsClosed() {
					t.Fatalf("rollback left a module open: %q", mod.Name())
				}
			}
			if err := comp.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(ctx); err != nil {
				t.Fatal(err)
			}
			wantWasm := int64(0)
			if mode != "reject bound signature" {
				wantWasm = 1 // The imported provider is still owned by the test.
			}
			if rt.LiveWasm.Load() != wantWasm || rt.LiveHost.Load() != 0 {
				t.Fatalf("close retained code: Wasm=%d host=%d", rt.LiveWasm.Load(), rt.LiveHost.Load())
			}
			// A caller may keep using the cache after the failed engine is closed.
			probe := NewEngine(ctx, WithRuntimeConfig(wazero.NewRuntimeConfig().WithCompilationCache(cache)))
			defer probe.Close(ctx)
			answer, err := probe.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, closeTestAnswer)))
			if err != nil {
				t.Fatal(err)
			}
			defer answer.Close(ctx)
			alive, err := answer.Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer alive.Close(ctx)
			callAnswer(t, ctx, alive)
		})
	}
}

func TestAdapterModules_ListenerClosesEngineDuringCompile(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	engine, rt, _ := trackAdapterCode(t)
	comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, fmt.Sprintf(adapterLifecycleWat, 0, 0, 0, 0))))
	if err != nil {
		t.Fatal(err)
	}
	defer comp.Close(context.Background())
	before := rt.LiveWasm.Load()
	var closed bool
	var closeErr error
	listener := experimental.FunctionListenerFactoryFunc(func(def api.FunctionDefinition) experimental.FunctionListener {
		if slices.Contains(def.ExportNames(), "batch") {
			closed = true
			closeErr = engine.Close(ctx)
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		inst, err := comp.Instantiate(experimental.WithFunctionListenerFactory(ctx, listener))
		if inst != nil {
			_ = inst.Close(ctx)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !closed || closeErr != nil || err == nil || !strings.Contains(err.Error(), "canonical host is closed") {
			t.Fatalf("listener close = %v (%v); instantiate = %v", closed, closeErr, err)
		}
	case <-ctx.Done():
		t.Fatal("engine close blocked in the function listener")
	}
	if rt.LiveHost.Load() != 0 || rt.LiveWasm.Load() != before {
		t.Fatalf("listener shutdown leaked compiled code: Wasm=%d (want %d), host=%d", rt.LiveWasm.Load(), before, rt.LiveHost.Load())
	}
}

// Track compiled handles through the public wazero interface, including when
// the caller owns a compilation cache that outlives this runtime.
func trackAdapterCode(t *testing.T) (*Engine, *testutil.Runtime, wazero.CompilationCache) {
	t.Helper()
	ctx := context.Background()
	cache := wazero.NewCompilationCache()
	t.Cleanup(func() { _ = cache.Close(ctx) })
	engine := NewEngine(ctx, WithRuntimeConfig(wazero.NewRuntimeConfig().WithCompilationCache(cache)))
	t.Cleanup(func() { _ = engine.Close(ctx) })
	rt := &testutil.Runtime{Runtime: engine.runtime}
	engine.runtime = rt
	engine.canonHost = canon.NewHost(rt)
	return engine, rt, cache
}
