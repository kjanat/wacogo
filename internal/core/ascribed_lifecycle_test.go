package core

import (
	"bytes"
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"weak"

	"github.com/kjanat/wacogo/wasmparser"
)

const ascribedLifecycleWat = `(component
  (import "empty" (instance $empty))
  (import "provider" (instance $provider (export "f" (func (result u32)))))
  (component $child
    (import "provider" (instance $borrowed (export "f" (func (result u32)))))
    (type $r (resource (rep i32)))
    (export "r" (type $r))
    (core module $m
      (func (export "f") (result i32) i32.const 7)
      (func (export "hidden")))
    (core instance $i (instantiate $m))
    (func (export "f") (result u32) (canon lift (core func $i "f")))
    (func (export "hidden") (canon lift (core func $i "hidden")))
    (export "borrowed" (instance $borrowed)))
  (instance $child-instance (instantiate $child (with "provider" (instance $provider))))
  (alias export $child-instance "r" (type $r))
  (export "full" (instance $child-instance))
  (export $narrow "narrow" (instance $child-instance) (instance
    (alias outer 1 $r (type $resource))
    (export "r" (type (eq $resource)))
    (export "f" (func (result u32)))))
  (export $again "again" (instance $narrow) (instance
    (alias outer 1 $r (type $resource))
    (export "r" (type (eq $resource)))
    (export "f" (func (result u32)))))
  (component $relay
    (import "empty" (instance $empty))
    (import "view" (instance $view
      (export "r" (type (sub resource)))
      (export "f" (func (result u32)))))
    (export "empty" (instance $empty))
    (export "view" (instance $view)))
  (instance $relay-instance (instantiate $relay
    (with "empty" (instance $empty))
    (with "view" (instance $again))))
  (alias export $relay-instance "view" (instance $relayed))
  (export "relay" (instance $relay-instance))
  (export "relayed" (instance $relayed)))`

func loadAscribedLifecycle(t *testing.T) (*Component, *ComponentInstance) {
	t.Helper()
	ctx := t.Context()
	engine := NewEngine(ctx)
	t.Cleanup(func() { _ = engine.Close(context.Background()) })
	read := func(wat string) *Component {
		t.Helper()
		c, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, wat)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	provider, err := read(closeTestAnswer).Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
	return read(ascribedLifecycleWat), provider
}

func TestAscribedInstanceExportClose(t *testing.T) {
	for first, name := range []string{"full", "narrow", "again", "relayed", "relay.view"} {
		t.Run(name+" first", func(t *testing.T) {
			ctx := t.Context()
			comp, provider := loadAscribedLifecycle(t)
			inst, err := comp.Instantiate(ctx, WithInstanceImport("provider", provider))
			if err != nil {
				t.Fatal(err)
			}
			full, narrow := inst.ExportedInstance("full"), inst.ExportedInstance("narrow")
			if full == nil || narrow == nil || narrow.runtimeOwner() != full.runtimeOwner() {
				t.Fatal("expected two views of one runtime instance")
			}
			assertInstanceTypesAligned(t, "root", inst)
			relay := inst.ExportedInstance("relay")
			if relay == nil || relay.ExportedInstance("empty") == nil || relay.wpInstance.ExportedInstance("empty") == nil {
				t.Fatal("the omitted runtime-empty instance lost its position through the relay")
			}
			resource, ok := full.exportedType("r").(*TypeResource)
			if !ok || resource == nil {
				t.Fatal("full view lacks the resource")
			}
			views := []*ComponentInstance{full, narrow, inst.ExportedInstance("again"),
				inst.ExportedInstance("relayed"), relay.ExportedInstance("view")}
			for _, view := range views[1:] {
				if view == nil || view.exportedType("r") != resource || view.ExportedFunc("hidden") != nil ||
					view.ExportedInstance("borrowed") != nil {
					t.Fatal("re-export lost resource identity or exposed a hidden export")
				}
				callAnswer(t, ctx, view)
			}
			if full.ExportedFunc("hidden") == nil || full.ExportedInstance("borrowed") != provider {
				t.Fatal("the unrestricted view lost the hidden function or the borrowed provider")
			}
			owner := full.runtimeOwner()
			closed := 0
			owner.owned = append(owner.owned, teardownCloser{close: func(context.Context) error {
				closed++
				return nil
			}})
			var destroyed []uint32
			resource.dtor = func(_ context.Context, rep uint32) error {
				destroyed = append(destroyed, rep)
				return nil
			}
			table := narrow.runtimeOwner().resources
			if err := table.IssueOwn(resource, 1).Drop(ctx); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(destroyed, []uint32{1}) {
				t.Fatalf("destructors on return from Drop = %v, want [1]", destroyed)
			}
			handle := table.IssueOwn(resource, 2)
			retained := make([]*ExportedFunc, 0, len(views))
			for _, view := range views {
				retained = append(retained, view.ExportedFunc("f"))
			}
			if err := views[first].Close(ctx); err != nil {
				t.Fatal(err)
			}
			if closed != 1 {
				t.Fatalf("first close ran teardown %d times, want once", closed)
			}
			for _, f := range retained {
				if _, err := f.Call(ctx); err == nil || !strings.Contains(err.Error(), "closed") {
					t.Fatalf("retained callable after close: %v", err)
				}
			}
			if err := handle.Drop(ctx); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Fatalf("dropping an outstanding handle after close: %v", err)
			}
			for i, view := range views {
				if i == first {
					continue
				}
				if err := view.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := inst.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if closed != 1 || !slices.Equal(destroyed, []uint32{1}) {
				t.Fatalf("teardowns=%d, destructors=%v; want one teardown and only the explicit drop", closed, destroyed)
			}
			callAnswer(t, ctx, provider)
		})
	}
}

func TestCloseAscribedImport(t *testing.T) {
	ctx := t.Context()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)
	load := func(wat string) *Component {
		t.Helper()
		c, err := engine.LoadComponent(ctx, bytes.NewReader(parseLinkWat(t, wat)))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	source := load(closeTestAnswer)
	defer source.Close(ctx)
	consumer := load(`(component
		(import "provider" (instance $p (export "f" (func (result u32)))))
		(export "view" (instance $p) (instance)))`)
	defer consumer.Close(ctx)
	provider, err := source.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close(ctx)
	client, err := consumer.Instantiate(ctx, WithInstanceImport("provider", provider))
	if err != nil {
		t.Fatal(err)
	}
	view := client.ExportedInstance("view")
	if view == nil || view.ExportedFunc("f") != nil {
		t.Fatal("expected a restricted view of the imported provider")
	}
	f := provider.ExportedFunc("f")
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	callAnswer(t, ctx, provider)
	if err := view.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(ctx); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("provider call after closing its re-export: %v", err)
	}
}

func TestAscribedInstanceGraphsAreCollectible(t *testing.T) {
	ctx := t.Context()
	comp, provider := loadAscribedLifecycle(t)
	var instances []weak.Pointer[ComponentInstance]
	var types []weak.Pointer[wasmparser.InstanceType]
	for range 24 {
		inst, err := comp.Instantiate(ctx, WithInstanceImport("provider", provider))
		if err != nil {
			t.Fatal(err)
		}
		relay := inst.ExportedInstance("relay")
		if relay == nil {
			t.Fatal("missing relay")
		}
		for _, candidate := range []*ComponentInstance{inst, inst.ExportedInstance("full"),
			inst.ExportedInstance("narrow"), inst.ExportedInstance("again"), relay,
			inst.ExportedInstance("relayed"), relay.ExportedInstance("view")} {
			if candidate == nil {
				t.Fatal("missing runtime instance")
			}
			instances = append(instances, weak.Make(candidate))
			types = append(types, weak.Make(candidate.wpInstance))
		}
		if err := inst.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	for i, ptr := range instances {
		if ptr.Value() != nil {
			t.Fatalf("live component or provider retained instance %d after teardown", i)
		}
	}
	for i, ptr := range types {
		if ptr.Value() != nil {
			t.Fatalf("live component or provider retained instance type %d after teardown", i)
		}
	}
	runtime.KeepAlive(comp)
	runtime.KeepAlive(provider)
}
