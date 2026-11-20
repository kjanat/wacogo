package fixturetests_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/counterhost"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/counters"
)

const wasmCounterhostWAT = `(component
  (core module $state
    (memory (export "memory") 1)
    (global $next (mut i32) (i32.const 16))
    (global $drops (mut i32) (i32.const 0))
    (func (export "alloc") (result i32)
      (local $p i32)
      global.get $next
      local.tee $p
      i32.const 4
      i32.add
      global.set $next
      local.get $p
    )
    (func (export "dtor") (param i32)
      global.get $drops
      i32.const 1
      i32.add
      global.set $drops
    )
    (func (export "drops") (result i32) global.get $drops)
  )
  (core instance $state (instantiate $state))
  (alias core export $state "dtor" (core func $dtor))
  (type $counter (resource (rep i32) (dtor (func $dtor))))
  (core func $resource-new (canon resource.new $counter))

  (core module $impl
    (import "state" "memory" (memory 1))
    (import "state" "alloc" (func $alloc (result i32)))
    (import "" "new" (func $new (param i32) (result i32)))
    (func (export "ctor") (param $v i32) (result i32)
      (local $p i32)
      call $alloc
      local.tee $p
      local.get $v
      i32.store
      local.get $p
      call $new
    )
    (func (export "increment") (param $rep i32)
      local.get $rep
      local.get $rep
      i32.load
      i32.const 1
      i32.add
      i32.store
    )
    (func (export "current") (param $rep i32) (result i32)
      local.get $rep
      i32.load
    )
  )
  (core instance $new-inst (export "new" (func $resource-new)))
  (core instance $impl (instantiate $impl
    (with "state" (instance $state))
    (with "" (instance $new-inst))))

  (alias core export $impl "ctor" (core func $ctor))
  (alias core export $impl "increment" (core func $increment))
  (alias core export $impl "current" (core func $current))
  (alias core export $state "drops" (core func $drops))

  (func $ctor-lifted (param "initial" u32) (result (own $counter)) (canon lift (core func $ctor)))
  (func $increment-lifted (param "self" (borrow $counter)) (canon lift (core func $increment)))
  (func $current-lifted (param "self" (borrow $counter)) (result u32) (canon lift (core func $current)))
  (func $drops-lifted (result u32) (canon lift (core func $drops)))

  (export $c "counter" (type $counter))
  (export "[constructor]counter" (func $ctor-lifted) (func (param "initial" u32) (result (own $c))))
  (export "[method]counter.increment" (func $increment-lifted) (func (param "self" (borrow $c))))
  (export "[method]counter.current" (func $current-lifted) (func (param "self" (borrow $c)) (result u32)))
  (export "drops" (func $drops-lifted))
)`

const wasmCountersWAT = `(component
  (core module $state
    (memory (export "memory") 1)
    (data (i32.const 64) "too large")
    (global $drops (mut i32) (i32.const 0))
    (func (export "dtor") (param i32)
      global.get $drops
      i32.const 1
      i32.add
      global.set $drops
    )
    (func (export "drops") (result i32) global.get $drops)
  )
  (core instance $state (instantiate $state))
  (alias core export $state "dtor" (core func $dtor))
  (alias core export $state "memory" (core memory $mem))
  (type $counter (resource (rep i32) (dtor (func $dtor))))
  (core func $resource-new (canon resource.new $counter))

  (core module $impl
    (import "state" "memory" (memory 1))
    (import "" "new" (func $new (param i32) (result i32)))
    (func (export "ctor") (param $start i32) (result i32)
      local.get $start
      i32.const 100
      i32.gt_u
      if
        i32.const 16
        i32.const 1
        i32.store8
        i32.const 20
        i32.const 64
        i32.store
        i32.const 24
        i32.const 9
        i32.store
      else
        i32.const 16
        i32.const 0
        i32.store8
        i32.const 20
        local.get $start
        call $new
        i32.store
      end
      i32.const 16
    )
    (func (export "get") (param $rep i32) (result i32)
      local.get $rep
    )
    (func (export "add") (param $rep i32) (param $n i32) (result i32)
      local.get $rep
      local.get $n
      i32.add
    )
  )
  (core instance $new-inst (export "new" (func $resource-new)))
  (core instance $impl (instantiate $impl
    (with "state" (instance $state))
    (with "" (instance $new-inst))))

  (alias core export $impl "ctor" (core func $ctor))
  (alias core export $impl "get" (core func $get))
  (alias core export $impl "add" (core func $add))
  (alias core export $state "drops" (core func $drops))

  (func $ctor-lifted (param "start" u32) (result (result (own $counter) (error string)))
    (canon lift (core func $ctor) (memory $mem)))
  (func $get-lifted (param "self" (borrow $counter)) (result u32) (canon lift (core func $get)))
  (func $add-lifted (param "self" (borrow $counter)) (param "n" u32) (result u32) (canon lift (core func $add)))
  (func $drops-lifted (result u32) (canon lift (core func $drops)))

  (export $c "counter" (type $counter))
  (export "[constructor]counter" (func $ctor-lifted)
    (func (param "start" u32) (result (result (own $c) (error string)))))
  (export "[method]counter.get" (func $get-lifted) (func (param "self" (borrow $c)) (result u32)))
  (export "[method]counter.add" (func $add-lifted) (func (param "self" (borrow $c)) (param "n" u32) (result u32)))
  (export "drops" (func $drops-lifted))
)`

func instantiateWAT(t *testing.T, ctx context.Context, e *wacogo.Engine, wat string) *wacogo.ComponentInstance {
	t.Helper()
	comp, err := e.LoadComponent(ctx, bytes.NewReader(watToBinary(t, wat)))
	if err != nil {
		t.Fatalf("LoadComponent: %v", err)
	}
	inst, err := comp.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	t.Cleanup(func() { inst.Close(ctx) })
	return inst
}

func newBareHost(t *testing.T, ctx context.Context, e *wacogo.Engine) *host.ComponentInstance {
	t.Helper()
	comp, err := e.NewHostBuilder("caller").Build(ctx)
	if err != nil {
		t.Fatalf("host Build: %v", err)
	}
	t.Cleanup(func() { comp.Close(ctx) })
	inst, err := comp.Instantiate(ctx)
	if err != nil {
		t.Fatalf("host Instantiate: %v", err)
	}
	t.Cleanup(func() { inst.Close(ctx) })
	return inst
}

func wasmDrops(t *testing.T, ctx context.Context, inst *wacogo.ComponentInstance) uint32 {
	t.Helper()
	got, err := inst.ExportedFunc("drops").Call(ctx)
	if err != nil {
		t.Fatalf("drops: %v", err)
	}
	return uint32(got[0].(wacogo.ValU32))
}

func TestCounterhost_WasmResourceMethods(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	inst := instantiateWAT(t, ctx, e, wasmCounterhostWAT)
	w := counterhost.WrapInstance(newBareHost(t, ctx, e), inst)

	c, err := w.NewCounter(ctx, 5)
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	if err := c.Increment(ctx); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	got, err := c.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != 6 {
		t.Errorf("Current = %d, want 6", got)
	}

	if n := wasmDrops(t, ctx, inst); n != 0 {
		t.Fatalf("drops before Drop = %d, want 0", n)
	}
	if err := c.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if n := wasmDrops(t, ctx, inst); n != 1 {
		t.Errorf("drops after Drop = %d, want 1", n)
	}
	if _, err := c.Current(ctx); err == nil {
		t.Error("Current after Drop: got nil error")
	}
}

func TestCounters_WasmFallibleCtorMethods(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	inst := instantiateWAT(t, ctx, e, wasmCountersWAT)
	w := counters.WrapInstance(newBareHost(t, ctx, e), inst)

	r, err := w.NewCounter(ctx, 42)
	if err != nil {
		t.Fatalf("NewCounter(42): %v", err)
	}
	ok, isOk := r.(counters.ResultCounterStringOk)
	if !isOk {
		t.Fatalf("NewCounter(42) = %#v, want Ok", r)
	}
	got, err := ok.Value.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != 42 {
		t.Errorf("Get = %d, want 42", got)
	}
	sum, err := ok.Value.Add(ctx, 8)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sum != 50 {
		t.Errorf("Add(8) = %d, want 50", sum)
	}
	if err := ok.Value.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if n := wasmDrops(t, ctx, inst); n != 1 {
		t.Errorf("drops after Drop = %d, want 1", n)
	}

	r, err = w.NewCounter(ctx, 500)
	if err != nil {
		t.Fatalf("NewCounter(500): %v", err)
	}
	if got, want := r, (counters.ResultCounterStringErr{Value: "too large"}); got != want {
		t.Errorf("NewCounter(500) = %#v, want %#v", got, want)
	}
}
