package fixturetests_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/counters"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/failures"
	"github.com/kjanat/wacogo/internal/witgen/testdata/genfixtures/example/demo/registry"
)

type countersCounter struct {
	v     uint32
	drops *int
}

func (c *countersCounter) Get(ctx context.Context) (uint32, error) { return c.v, nil }

func (c *countersCounter) Add(ctx context.Context, n uint32) (uint32, error) { return c.v + n, nil }

func (c *countersCounter) Drop(_ context.Context) error {
	*c.drops++
	return nil
}

type countersGauge struct{ level int32 }

func (g *countersGauge) Level(ctx context.Context) (int32, error) { return g.level, nil }

type myCounters struct{ drops int }

func (m *myCounters) NewCounter(ctx context.Context, start uint32) (counters.ResultCounterString, error) {
	if start > 100 {
		return counters.ResultCounterStringErr{Value: "too large"}, nil
	}
	return counters.ResultCounterStringOk{Value: counters.NewCounterHandle(&countersCounter{v: start, drops: &m.drops})}, nil
}

func (m *myCounters) NewGauge(ctx context.Context, level int32) (counters.ResultGaugeFailuresFailure, error) {
	switch {
	case level < 0:
		return counters.ResultGaugeFailuresFailureErr{Value: failures.FailureNegative{Value: level}}, nil
	case level > 100:
		return counters.ResultGaugeFailuresFailureErr{Value: failures.FailureTooLarge{}}, nil
	}
	return counters.ResultGaugeFailuresFailureOk{Value: counters.NewGaugeHandle(&countersGauge{level: level})}, nil
}

type myFailures struct{}

func newCountersHost(t *testing.T, ctx context.Context, e *wacogo.Engine, impl counters.Counters) *host.ComponentInstance {
	t.Helper()
	failuresFac, err := failures.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("failures.NewFactory: %v", err)
	}
	t.Cleanup(func() { failuresFac.Close(ctx) })
	failuresInst, err := failuresFac.NewInstance(ctx, myFailures{}, nil)
	if err != nil {
		t.Fatalf("failures.NewInstance: %v", err)
	}
	t.Cleanup(func() { failuresInst.Close(ctx) })

	fac, err := counters.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("counters.NewFactory: %v", err)
	}
	t.Cleanup(func() { fac.Close(ctx) })
	inst, err := fac.NewInstance(ctx, impl, &counters.Deps{Failures: failuresInst.Core()})
	if err != nil {
		t.Fatalf("counters.NewInstance: %v", err)
	}
	t.Cleanup(func() { inst.Close(ctx) })
	return inst
}

func TestCounters_FallibleCtorFromGuest(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	impl := &myCounters{}
	hostInst := newCountersHost(t, ctx, e, impl)

	importerWAT := `(component
  (import "h" (instance $h
    (export "counter" (type $c (sub resource)))
    (export "[constructor]counter" (func (param "start" u32) (result (result (own $c) (error string)))))
    (export "[method]counter.get" (func (param "self" (borrow $c)) (result u32)))
  ))
  (alias export $h "counter" (type $c))
  (alias export $h "[constructor]counter" (func $new))
  (alias export $h "[method]counter.get" (func $get))

  (core module $libc
    (memory (export "memory") 1)
    (global $next (mut i32) (i32.const 1024))
    (func (export "realloc") (param i32 i32 i32 i32) (result i32)
      (local $p i32)
      global.get $next
      local.get 2
      i32.add
      i32.const 1
      i32.sub
      i32.const 0
      local.get 2
      i32.sub
      i32.and
      local.tee $p
      local.get 3
      i32.add
      global.set $next
      local.get $p
    )
  )
  (core instance $libc (instantiate $libc))
  (alias core export $libc "memory" (core memory $mem))
  (alias core export $libc "realloc" (core func $realloc))

  (core func $new-lowered (canon lower (func $new) (memory $mem) (realloc $realloc)))
  (core func $get-lowered (canon lower (func $get)))
  (core func $drop (canon resource.drop $c))

  (core module $m
    (import "libc" "memory" (memory 1))
    (import "" "new" (func $new (param i32 i32)))
    (import "" "get" (func $get (param i32) (result i32)))
    (import "" "drop" (func $drop (param i32)))

    (func (export "get-after-new") (param $start i32) (result i32)
      (local $h i32)
      (local $v i32)
      local.get $start
      i32.const 16
      call $new
      i32.const 16
      i32.load8_u
      if
        unreachable
      end
      i32.const 20
      i32.load
      local.tee $h
      call $get
      local.set $v
      local.get $h
      call $drop
      local.get $v
    )

    (func (export "err-msg") (param $start i32) (result i32)
      local.get $start
      i32.const 16
      call $new
      i32.const 16
      i32.load8_u
      i32.eqz
      if
        unreachable
      end
      i32.const 20
    )
  )
  (core instance $bridge
    (export "new" (func $new-lowered))
    (export "get" (func $get-lowered))
    (export "drop" (func $drop)))
  (core instance $i (instantiate $m
    (with "libc" (instance $libc))
    (with "" (instance $bridge))))
  (alias core export $i "get-after-new" (core func $gan))
  (alias core export $i "err-msg" (core func $em))
  (func (export "get-after-new") (param "start" u32) (result u32) (canon lift (core func $gan)))
  (func (export "err-msg") (param "start" u32) (result string) (canon lift (core func $em) (memory $mem)))
)`
	bin := watToBinary(t, importerWAT)
	comp, err := e.LoadComponent(ctx, bytes.NewReader(bin))
	if err != nil {
		t.Fatalf("LoadComponent: %v", err)
	}
	inst, err := comp.Instantiate(ctx, wacogo.WithInstanceImport("h", hostInst.Core()))
	if err != nil {
		t.Fatalf("Instantiate importer: %v", err)
	}
	defer inst.Close(ctx)

	got, err := inst.ExportedFunc("get-after-new").Call(ctx, wacogo.ValU32(42))
	if err != nil {
		t.Fatalf("get-after-new(42): %v", err)
	}
	if u := uint32(got[0].(wacogo.ValU32)); u != 42 {
		t.Errorf("get-after-new(42) = %d, want 42", u)
	}
	if impl.drops != 1 {
		t.Errorf("counter drops = %d, want 1", impl.drops)
	}

	got, err = inst.ExportedFunc("err-msg").Call(ctx, wacogo.ValU32(500))
	if err != nil {
		t.Fatalf("err-msg(500): %v", err)
	}
	if s := string(got[0].(wacogo.ValString)); s != "too large" {
		t.Errorf("err-msg(500) = %q, want %q", s, "too large")
	}
}

func TestCounters_FallibleCtorThroughWrapper(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	hostInst := newCountersHost(t, ctx, e, &myCounters{})
	w := counters.WrapInstance(hostInst, hostInst.Core())

	r, err := w.NewCounter(ctx, 7)
	if err != nil {
		t.Fatalf("NewCounter(7): %v", err)
	}
	ok, isOk := r.(counters.ResultCounterStringOk)
	if !isOk {
		t.Fatalf("NewCounter(7) = %#v, want Ok", r)
	}
	v, err := ok.Value.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != 7 {
		t.Errorf("Get = %d, want 7", v)
	}

	r, err = w.NewCounter(ctx, 500)
	if err != nil {
		t.Fatalf("NewCounter(500): %v", err)
	}
	if got, want := r, (counters.ResultCounterStringErr{Value: "too large"}); got != want {
		t.Errorf("NewCounter(500) = %#v, want %#v", got, want)
	}

	g, err := w.NewGauge(ctx, 9)
	if err != nil {
		t.Fatalf("NewGauge(9): %v", err)
	}
	gok, isOk := g.(counters.ResultGaugeFailuresFailureOk)
	if !isOk {
		t.Fatalf("NewGauge(9) = %#v, want Ok", g)
	}
	level, err := gok.Value.Level(ctx)
	if err != nil {
		t.Fatalf("Level: %v", err)
	}
	if level != 9 {
		t.Errorf("Level = %d, want 9", level)
	}

	for _, tc := range []struct {
		level int32
		want  failures.Failure
	}{
		{-2, failures.FailureNegative{Value: -2}},
		{1000, failures.FailureTooLarge{}},
	} {
		g, err := w.NewGauge(ctx, tc.level)
		if err != nil {
			t.Fatalf("NewGauge(%d): %v", tc.level, err)
		}
		if got, want := g, (counters.ResultGaugeFailuresFailureErr{Value: tc.want}); got != want {
			t.Errorf("NewGauge(%d) = %#v, want %#v", tc.level, got, want)
		}
	}
}

type myRegistry struct{}

func (myRegistry) Adopt(ctx context.Context, g *counters.GaugeHandle) (int32, error) {
	return g.Level(ctx)
}

func TestRegistry_ImportsFallibleCtorResource(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	failuresFac, err := failures.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("failures.NewFactory: %v", err)
	}
	defer failuresFac.Close(ctx)
	failuresInst, err := failuresFac.NewInstance(ctx, myFailures{}, nil)
	if err != nil {
		t.Fatalf("failures.NewInstance: %v", err)
	}
	defer failuresInst.Close(ctx)

	countersFac, err := counters.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("counters.NewFactory: %v", err)
	}
	defer countersFac.Close(ctx)
	countersInst, err := countersFac.NewInstance(ctx, &myCounters{}, &counters.Deps{Failures: failuresInst.Core()})
	if err != nil {
		t.Fatalf("counters.NewInstance: %v", err)
	}
	defer countersInst.Close(ctx)

	fac, err := registry.NewFactory(ctx, e)
	if err != nil {
		t.Fatalf("registry.NewFactory: %v", err)
	}
	defer fac.Close(ctx)
	inst, err := fac.NewInstance(ctx, myRegistry{}, &registry.Deps{Counters: countersInst.Core(), Failures: failuresInst.Core()})
	if err != nil {
		t.Fatalf("registry.NewInstance: %v", err)
	}
	defer inst.Close(ctx)

	w := registry.WrapInstance(inst, inst.Core())
	level, err := w.Adopt(ctx, counters.NewGaugeHandleIn(countersInst, &countersGauge{level: 4}))
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if level != 4 {
		t.Errorf("Adopt = %d, want 4", level)
	}
}
