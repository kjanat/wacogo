package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/experimental"
)

type teardownAllocator struct {
	mu          sync.Mutex
	live        uint64
	allocations int
	freed       []uint64
}

func (a *teardownAllocator) Allocate(_, _ uint64) experimental.LinearMemory {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allocations++
	return &teardownMemory{allocator: a}
}

type teardownMemory struct {
	allocator *teardownAllocator
	data      []byte
}

func (m *teardownMemory) Reallocate(size uint64) []byte {
	m.allocator.mu.Lock()
	defer m.allocator.mu.Unlock()
	m.allocator.live -= uint64(len(m.data))
	data := make([]byte, size)
	copy(data, m.data)
	m.data = data
	m.allocator.live += size
	return data
}

func (m *teardownMemory) Free() {
	m.allocator.mu.Lock()
	defer m.allocator.mu.Unlock()
	size := uint64(len(m.data))
	m.allocator.live -= size
	m.allocator.freed = append(m.allocator.freed, size)
	m.data = nil
}

func (a *teardownAllocator) check(t *testing.T, allocations int, live uint64, freed []uint64) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.allocations != allocations || a.live != live || !reflect.DeepEqual(a.freed, freed) {
		t.Fatalf("memory: allocations=%d live=%d freed=%v; want allocations=%d live=%d freed=%v", a.allocations, a.live, a.freed, allocations, live, freed)
	}
}

const nestedMemoryComponent = `(component
  (core module $first (memory 1))
  (core instance (instantiate $first))
  (component $child
    (component $grandchild
      (core module $memory (memory 2))
      (core instance (instantiate $memory)))
    (instance (instantiate $grandchild)))
  (instance $child-instance (instantiate $child))
  (export "child" (instance $child-instance))
  (core module $last (memory 3))
  (core instance (instantiate $last)))`

func TestNestedInstancesCloseInReverseCreationOrder(t *testing.T) {
	allocator := &teardownAllocator{}
	ctx := experimental.WithMemoryAllocator(t.Context(), allocator)
	e := NewEngine(ctx)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	component, err := e.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, nestedMemoryComponent)))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := component.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allocator.check(t, 3, 6*65536, nil)
	if err := instance.Close(ctx); err != nil {
		t.Fatal(err)
	}
	allocator.check(t, 3, 0, []uint64{3 * 65536, 2 * 65536, 65536})
	if err := instance.Close(ctx); err != nil {
		t.Fatal(err)
	}
	allocator.check(t, 3, 0, []uint64{3 * 65536, 2 * 65536, 65536})
}

func TestFailedParentClosesNestedInstances(t *testing.T) {
	for _, phase := range []string{"plan", "exports"} {
		t.Run(phase, func(t *testing.T) {
			allocator := &teardownAllocator{}
			ctx := experimental.WithMemoryAllocator(t.Context(), allocator)
			e := NewEngine(ctx)
			t.Cleanup(func() { _ = e.Close(context.Background()) })
			source := nestedMemoryComponent
			if phase == "plan" {
				source = source[:len(source)-1] + `(core module $trap (func $start unreachable) (start $start)) (core instance (instantiate $trap)))`
			}
			component, err := e.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, source)))
			if err != nil {
				t.Fatal(err)
			}
			if phase == "exports" {
				component.exports = append(component.exports, ExportDesc{Name: "missing", Kind: SortFunc, Index: 999})
			}
			instance, err := component.Instantiate(ctx)
			if err == nil || instance != nil {
				t.Fatalf("invalid parent succeeded: instance=%v err=%v", instance, err)
			}
			allocator.check(t, 3, 0, []uint64{3 * 65536, 2 * 65536, 65536})
		})
	}
}

func TestClosingParentPreservesBorrowedInstance(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", fail), func(t *testing.T) {
			checkBorrowedInstanceAfterTeardown(t, fail)
		})
	}
}

func checkBorrowedInstanceAfterTeardown(t *testing.T, fail bool) {
	t.Helper()
	allocator := &teardownAllocator{}
	ctx := experimental.WithMemoryAllocator(t.Context(), allocator)
	e := NewEngine(ctx)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	provider, err := e.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, `(component (core module $m (memory 1)) (core instance (instantiate $m)))`)))
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := provider.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := `(component
  (type $empty (instance))
  (import "borrowed" (instance $borrowed (type $empty)))
  (component $owned
    (core module $memory (memory 2))
    (core instance (instantiate $memory)))
  (instance (instantiate $owned))
  (export "alias" (instance $borrowed)))`
	if fail {
		source = source[:len(source)-1] + `(core module $trap (func $start unreachable) (start $start)) (core instance (instantiate $trap)))`
	}
	consumer, err := e.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, source)))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := consumer.Instantiate(ctx, WithInstanceImport("borrowed", borrowed))
	if fail {
		if err == nil || parent != nil {
			t.Fatalf("trapping parent succeeded: instance=%v error=%v", parent, err)
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if err := parent.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	allocator.check(t, 2, 65536, []uint64{2 * 65536})
	if err := borrowed.Enter(ctx); err != nil {
		t.Fatalf("parent closed a borrowed instance: %v", err)
	}
	borrowed.Exit(ctx)
	if err := borrowed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	allocator.check(t, 2, 0, []uint64{2 * 65536, 65536})
}

type teardownCloser struct{ close func(context.Context) error }

func (c teardownCloser) Close(ctx context.Context) error { return c.close(ctx) }

func TestComponentCloseJoinsErrorsAndAllowsReentrantClose(t *testing.T) {
	firstErr, childErr := errors.New("first close"), errors.New("child close")
	var order []string
	parent := &ComponentInstance{}
	parent.owned = []instanceCloser{
		teardownCloser{close: func(context.Context) error { order = append(order, "first"); return firstErr }},
		&ComponentInstance{owned: []instanceCloser{teardownCloser{close: func(context.Context) error {
			order = append(order, "child")
			return childErr
		}}}},
		teardownCloser{close: func(ctx context.Context) error {
			order = append(order, "last")
			return parent.Close(ctx)
		}},
	}
	err := parent.Close(t.Context())
	if !errors.Is(err, firstErr) || !errors.Is(err, childErr) {
		t.Fatalf("close lost errors: %v", err)
	}
	if err := parent.Close(t.Context()); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"last", "child", "first"}) {
		t.Fatalf("close order/reentry: %v", order)
	}
}

type teardownStep struct {
	run func(context.Context, *instantiationState) error
}

func (s teardownStep) execute(ctx context.Context, state *instantiationState) error {
	return s.run(ctx, state)
}

func TestFailedInstantiationJoinsCleanupErrorsAfterCancellation(t *testing.T) {
	allocator := &teardownAllocator{}
	ctx, cancel := context.WithCancel(experimental.WithMemoryAllocator(t.Context(), allocator))
	defer cancel()
	e := NewEngine(ctx)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	component, err := e.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, nestedMemoryComponent)))
	if err != nil {
		t.Fatal(err)
	}
	primaryErr, cleanupErr := errors.New("instantiation failed"), errors.New("cleanup failed")
	var failed *ComponentInstance
	var closes int
	component.plan = append(component.plan, teardownStep{run: func(_ context.Context, state *instantiationState) error {
		failed = state.inst
		state.inst.owned = append(state.inst.owned, teardownCloser{close: func(ctx context.Context) error {
			closes++
			if err := ctx.Err(); !errors.Is(err, context.Canceled) {
				t.Errorf("cleanup lost cancellation: %v", err)
			}
			return cleanupErr
		}})
		cancel()
		return primaryErr
	}})
	instance, err := component.Instantiate(ctx)
	if instance != nil || !errors.Is(err, primaryErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("failed instantiation lost errors: instance=%v error=%v", instance, err)
	}
	allocator.check(t, 3, 0, []uint64{3 * 65536, 2 * 65536, 65536})
	if err := failed.Close(t.Context()); err != nil || closes != 1 {
		t.Fatalf("failed instance cleanup repeated: closes=%d error=%v", closes, err)
	}
}

func TestFailedInstantiationPreservesGuestDestructorCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			allocator := &teardownAllocator{}
			base := experimental.WithMemoryAllocator(t.Context(), allocator)
			e := NewEngine(base, WithRuntimeConfig(wazero.NewRuntimeConfig().WithCloseOnContextDone(true)))
			t.Cleanup(func() { _ = e.Close(context.Background()) })
			source := nestedMemoryComponent[:len(nestedMemoryComponent)-1] + `(core module $dtor (func (export "dtor"))) (core instance (instantiate $dtor)))`
			component, err := e.LoadComponent(base, bytes.NewReader(buildComponentBytes(t, source)))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(base)
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(base, time.Second)
				want = context.DeadlineExceeded
			}
			defer cancel()
			primaryErr := errors.New("instantiation failed")
			var calls int
			component.plan = append(component.plan, teardownStep{run: func(ctx context.Context, state *instantiationState) error {
				module := state.coreInstances[len(state.coreInstances)-1]
				state.inst.resources.IssueOwn(&TypeResource{instance: state.inst, dtor: func(ctx context.Context, _ uint32) error {
					calls++
					if !errors.Is(ctx.Err(), want) {
						t.Errorf("guest destructor lost cancellation: %v", ctx.Err())
					}
					_, err := module.ExportedFunction("dtor").Call(ctx)
					if err == nil {
						t.Error("guest destructor accepted canceled context")
					}
					return ctx.Err()
				}}, 1)
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return primaryErr
			}})
			instance, err := component.Instantiate(ctx)
			if instance != nil || !errors.Is(err, primaryErr) || !errors.Is(err, want) || calls != 1 {
				t.Fatalf("failed teardown: instance=%v calls=%d error=%v", instance, calls, err)
			}
			allocator.check(t, 3, 0, []uint64{3 * 65536, 2 * 65536, 65536})
		})
	}
}

func TestDescendantDestructorResourcesKeepProvidersAlive(t *testing.T) {
	for _, targetSibling := range []bool{false, true} {
		t.Run(fmt.Sprintf("sibling=%t", targetSibling), func(t *testing.T) {
			parent := &ComponentInstance{}
			child := &ComponentInstance{}
			sibling := &ComponentInstance{}
			parent.resources = NewResourceTable(parent)
			child.resources = NewResourceTable(child)
			sibling.resources = NewResourceTable(sibling)
			providerOpen := true
			child.owned = []instanceCloser{teardownCloser{close: func(context.Context) error {
				providerOpen = false
				return nil
			}}}
			// Sibling is visited before child; its newly acquired own needs
			// another subtree-wide pass, just like the parent's new own.
			parent.owned = []instanceCloser{child, sibling}
			target := parent
			if targetSibling {
				target = sibling
			}
			acquired, dropped := 0, 0
			provided := &TypeResource{instance: child, dtor: func(context.Context, uint32) error {
				if !providerOpen {
					return errors.New("provider closed before destructor")
				}
				dropped++
				return nil
			}}
			trigger := &TypeResource{instance: child, dtor: func(ctx context.Context, _ uint32) error {
				return target.RunInComponent(ctx, func() error {
					acquired++
					target.resources.IssueOwn(provided, 1)
					return nil
				})
			}}
			child.resources.IssueOwn(trigger, 1)
			for range 2 {
				if err := parent.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if acquired != 1 || dropped != 1 || providerOpen {
					t.Fatalf("acquired=%d dropped=%d providerOpen=%t", acquired, dropped, providerOpen)
				}
			}
		})
	}
}

func TestResourceDrainRetiresBeforeDestructorReentry(t *testing.T) {
	instance := &ComponentInstance{}
	instance.resources = NewResourceTable(instance)
	var drops []uint32
	resource := &TypeResource{instance: instance}
	resource.dtor = func(_ context.Context, rep uint32) error {
		drops = append(drops, rep)
		if rep == 1 {
			instance.resources.IssueOwn(resource, 2)
		}
		return nil
	}
	instance.resources.IssueOwn(resource, 1)
	for range 2 {
		if err := instance.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(drops, []uint32{1, 2}) {
			t.Fatalf("drops=%v", drops)
		}
	}
}
