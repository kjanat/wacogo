package core

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kjanat/wacogo/internal/testutil"
	"github.com/kjanat/wazero"
)

// TestInstantiate_NilImport checks a nil import is an error rather than a crash. A
// failed lookup such as ExportedInstance("typo") returns nil, so it's easy to pass.
func TestInstantiate_NilImport(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)

	for name, c := range map[string]struct {
		wat  string
		opt  InstantiateOption
		want string
	}{
		"instance":  {`(component (import "x" (instance)))`, WithInstanceImport("x", nil), "no instance supplied"},
		"func":      {`(component (import "x" (func)))`, WithFuncImport("x", nil), "no func supplied"},
		"module":    {`(component (import "x" (core module)))`, WithModuleImport("x", nil), "no module supplied"},
		"component": {`(component (import "x" (instance)))`, WithComponentImport("x", nil), "no component supplied"},
		"resource":  {`(component (import "x" (type (sub resource))))`, WithResourceTypeImport("x", nil, "r"), "no instance supplied"},
	} {
		t.Run(name, func(t *testing.T) {
			comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, c.wat)))
			if err != nil {
				t.Fatal(err)
			}
			defer comp.Close(ctx)
			_, err = comp.Instantiate(ctx, c.opt)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), `import "x"`) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// compileCounter counts the modules compiled through a runtime and those closed,
// which is otherwise only visible as memory that's never returned.
type compileCounter struct {
	wazero.Runtime
	compiled, closed int
}

type countedModule struct {
	wazero.CompiledModule
	c *compileCounter
}

func (c *compileCounter) CompileModule(ctx context.Context, binary []byte) (wazero.CompiledModule, error) {
	m, err := c.Runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, err
	}
	c.compiled++
	return countedModule{m, c}, nil
}

func (m countedModule) Close(ctx context.Context) error {
	m.c.closed++
	return m.CompiledModule.Close(ctx)
}

func countCompiles(e *Engine) *compileCounter {
	c := &compileCounter{Runtime: e.runtime}
	e.runtime = c
	return c
}

// TestLoadComponent_FailureClosesModules covers each way a load can fail after
// compiling, whichever component the modules are in.
func TestLoadComponent_FailureClosesModules(t *testing.T) {
	const invalid = `(type (func (param "a" u32) (param "a" u32)))`

	for name, c := range map[string]struct {
		wat      string
		limits   ParseLimits
		compiled int
	}{
		"invalid after a module": {
			wat:      `(component (core module) ` + invalid + `)`,
			compiled: 1,
		},
		"invalid in a nested component": {
			wat:      `(component (core module) (component (core module) ` + invalid + `))`,
			compiled: 2,
		},
		"after a nested component has ended": {
			wat:      `(component (component (core module)) (core module) ` + invalid + `)`,
			compiled: 2,
		},
		"instance limit": {
			wat: `(component (component $c (core module $m)
  (core instance (instantiate $m)) (core instance (instantiate $m)))
  (instance (instantiate $c)))`,
			limits:   ParseLimits{CoreInstances: 1},
			compiled: 1,
		},
		"root imports a component": {
			wat:      `(component (core module) (import "c" (component)))`,
			compiled: 1,
		},
		"root exports a component": {
			wat:      `(component (core module) (component $c) (export "c" (component $c)))`,
			compiled: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			engine := NewEngine(ctx, WithParseLimits(c.limits))
			defer engine.Close(ctx)
			counter := countCompiles(engine)

			if _, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, c.wat))); err == nil {
				t.Fatal("expected the load to fail")
			}
			if counter.compiled != c.compiled {
				t.Fatalf("compiled %d modules, want %d", counter.compiled, c.compiled)
			}
			if counter.closed != c.compiled {
				t.Fatalf("closed %d of %d modules", counter.closed, c.compiled)
			}
		})
	}
}

// TestComponentClose_ReleasesModules checks that a loaded component keeps what it
// compiled, nested components included, until Close releases each of them once,
// however many index spaces an outer alias put a module in.
func TestComponentClose_ReleasesModules(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)
	counter := countCompiles(engine)

	comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, `(component
  (core module $m)
  (component
    (alias outer 1 $m (core module))
    (core module)))`)))
	if err != nil {
		t.Fatal(err)
	}
	if counter.compiled != 2 || counter.closed != 0 {
		t.Fatalf("after load: compiled %d, closed %d", counter.compiled, counter.closed)
	}
	for range 2 {
		if err := comp.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if counter.closed != 2 {
			t.Fatalf("after Close: closed %d of 2", counter.closed)
		}
	}
	if _, err := comp.Instantiate(ctx); !errors.Is(err, ErrComponentClosed) {
		t.Fatalf("Instantiate after Close: got %v", err)
	}
}

const closeTestAnswer = `(component
  (core module $m (func (export "f") (result i32) i32.const 7))
  (core instance $i (instantiate $m))
  (func (export "f") (result u32) (canon lift (core func $i "f"))))`

func TestComponentClose_OverlappingInstantiation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		viaExport, sharedCode bool
	}{
		{"component with shared code", false, true},
		{"exported module with shared code", true, true},
		{"component with sole owner", false, false},
		{"exported module with sole owner", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			engine, rt, _ := trackAdapterCode(t)
			load := func(source string) *Component {
				t.Helper()
				c, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, source)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close(context.Background()) })
				return c
			}
			const prefix = `
  (import "seed" (func $seed (result u32)))
  (core func (canon lower (func $seed)))
  (core module $prefix)
  (core instance (instantiate $prefix))`
			const exported = `(component` + prefix + `
  (core module $m (func (export "f") (result i32) i32.const 7))
  (core instance $i (instantiate $m))
  (func (export "f") (result u32) (canon lift (core func $i "f")))
  (export "m" (core module $m)))`
			seed, err := load(strings.Replace(closeTestAnswer, "i32.const 7", "i32.const 99", 1)).Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer seed.Close(ctx)
			seedImport := WithFuncImport("seed", seed.ExportedFunc("f"))
			first := load(exported)
			var second *Component
			if tc.sharedCode {
				second = load(exported)
			}
			existing, err := first.Instantiate(ctx, seedImport)
			if err != nil {
				t.Fatal(err)
			}
			defer existing.Close(ctx)
			instantiate := func(ctx context.Context) (*ComponentInstance, error) { return first.Instantiate(ctx, seedImport) }
			if tc.viaExport {
				consumer := load(`(component
 (import "m" (core module $m (export "f" (func (result i32)))))` + prefix + `
 (core instance $i (instantiate $m))
 (func (export "f") (result u32) (canon lift (core func $i "f"))))`)
				instantiate = func(ctx context.Context) (*ComponentInstance, error) {
					return consumer.Instantiate(ctx, seedImport, WithModuleImport("m", existing.ExportedModule("m")))
				}
			}
			started := make(chan struct{})
			resume := make(chan struct{})
			type inFlightKey struct{}
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			inFlight := context.WithValue(waitCtx, inFlightKey{}, true)
			target := existing.ExportedModule("m").module
			rt.BeforeInstantiate = func(ctx context.Context, code *testutil.Code) error {
				if ctx.Value(inFlightKey{}) != true || code != target {
					return nil
				}
				close(started)
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			type outcome struct {
				inst *ComponentInstance
				err  error
			}
			result := make(chan outcome, 1)
			release := sync.OnceFunc(func() { close(resume) })
			var worker sync.WaitGroup
			moduleStart, hostStart := len(rt.Modules()), rt.LiveHost.Load()
			worker.Go(func() {
				inst, err := instantiate(inFlight)
				result <- outcome{inst, err}
			})
			defer func() {
				release()
				worker.Wait()
			}()
			select {
			case <-started:
			case <-waitCtx.Done():
				t.Fatal("instantiation did not reach its core module")
			}
			startedModules := rt.Modules()[moduleStart:]
			if len(startedModules) != 2 || rt.LiveHost.Load() != hostStart+1 {
				t.Fatal("expected an adapter and a core module before the pause")
			}
			if err := first.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := instantiate(ctx); !errors.Is(err, ErrComponentClosed) {
				t.Fatalf("new instantiation after close returned %v", err)
			}
			wasmAfterClose := rt.LiveWasm.Load()
			release()
			var got outcome
			select {
			case got = <-result:
			case <-waitCtx.Done():
				t.Fatal("in-flight instantiation stopped making progress")
			}
			rt.BeforeInstantiate = nil
			callAnswer(t, ctx, existing)
			if second == nil {
				if got.inst != nil || got.err == nil || !strings.Contains(got.err.Error(), "instantiate module") {
					t.Fatalf("in-flight instantiation with released code: instance=%v, err=%v", got.inst, got.err)
				}
				for _, mod := range rt.Modules()[moduleStart:] {
					if !mod.IsClosed() {
						t.Fatalf("rollback left module %q open", mod.Name())
					}
				}
				if rt.LiveHost.Load() != hostStart || rt.LiveWasm.Load() != wasmAfterClose {
					t.Fatal("rollback left a compiled handle live")
				}
				return
			}
			// The operation passed the owner check before close. The second
			// component still holds the same compiled bytes, so it can finish.
			if got.err != nil {
				t.Fatalf("in-flight instantiation with shared code: %v", got.err)
			}
			defer got.inst.Close(ctx)
			callAnswer(t, ctx, got.inst)
			sibling, err := second.Instantiate(ctx, seedImport)
			if err != nil {
				t.Fatal(err)
			}
			defer sibling.Close(ctx)
			callAnswer(t, ctx, sibling)
		})
	}
}

func callAnswer(t *testing.T, ctx context.Context, inst *ComponentInstance) {
	t.Helper()
	got, err := inst.ExportedFunc("f").Call(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != ValU32(7) {
		t.Fatalf("got %v", got)
	}
}

// TestComponentClose_InstancesKeepRunning checks an instance outlives its
// Component, including once the collector has had a chance to run.
func TestComponentClose_InstancesKeepRunning(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)

	comp, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, closeTestAnswer)))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := comp.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := comp.Close(ctx); err != nil {
		t.Fatal(err)
	}
	comp = nil
	runtime.GC()
	runtime.GC()
	callAnswer(t, ctx, inst)
	if err := inst.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestComponentClose_SharedCodeSurvives checks that closing a component leaves
// another compiled from the same bytes working, as they share compiled code.
func TestComponentClose_SharedCodeSurvives(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)
	bin := parseLinkWat(t, closeTestAnswer)

	first, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	inst, err := second.Instantiate(ctx)
	if err != nil {
		t.Fatalf("instantiating the other component: %v", err)
	}
	callAnswer(t, ctx, inst)

	// A failed load of the same module must not take the code away either.
	if _, err := engine.LoadComponent(ctx, bytes.NewReader(append(bytes.Clone(bin), 7, 2, 1, 0))); err == nil {
		t.Fatal("expected the load to fail")
	}
	inst2, err := second.Instantiate(ctx)
	if err != nil {
		t.Fatalf("instantiating after a failed load: %v", err)
	}
	callAnswer(t, ctx, inst2)
}

// TestComponentClose_ExportedModule checks that a module taken from an instance
// fails cleanly once its Component is closed.
func TestComponentClose_ExportedModule(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)

	bin := parseLinkWat(t, `(component
  (core module $m)
  (export "m" (core module $m)))`)
	provider, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	other, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := provider.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mod := inst.ExportedModule("m")
	if mod == nil {
		t.Fatal("no exported module")
	}
	consumer, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, `(component
  (import "m" (core module $m))
  (core instance (instantiate $m)))`)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := consumer.Instantiate(ctx, WithModuleImport("m", mod)); err != nil {
		t.Fatalf("before Close: %v", err)
	}
	if err := provider.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Instantiate(ctx, WithModuleImport("m", mod)); !errors.Is(err, ErrComponentClosed) {
		t.Fatalf("instantiating the closed provider's module: %v", err)
	}
	otherInst, err := other.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.Instantiate(ctx, WithModuleImport("m", otherInst.ExportedModule("m"))); err != nil {
		t.Fatalf("instantiating the other provider's module: %v", err)
	}
}
