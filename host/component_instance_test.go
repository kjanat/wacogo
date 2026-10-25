package host

import (
	"context"
	"errors"
	"testing"

	"github.com/kjanat/wacogo/internal/core"
)

func TestComponentInstance_RegisterResourceRoundTrip(t *testing.T) {
	h := &ComponentInstance{extTable: &externTable{}}

	type obj struct{ name string }
	a := &obj{name: "a"}
	b := &obj{name: "b"}

	ehA := h.RegisterResource(a)
	ehB := h.RegisterResource(b)
	if ehA == ehB {
		t.Fatal("two registrations should produce distinct handles")
	}
	if ehA == 0 || ehB == 0 {
		t.Fatal("ExternHandle should be 1-indexed (0 reserved)")
	}

	gotA, ok := h.LookupResource(ehA)
	if !ok || gotA != a {
		t.Errorf("LookupResource(ehA) = (%v, %v), want (%p, true)", gotA, ok, a)
	}
	gotB, ok := h.LookupResource(ehB)
	if !ok || gotB != b {
		t.Errorf("LookupResource(ehB) = (%v, %v), want (%p, true)", gotB, ok, b)
	}

	releasedA, ok := h.releaseResource(ehA)
	if !ok || releasedA != a {
		t.Errorf("releaseResource(ehA) = (%v, %v), want (%p, true)", releasedA, ok, a)
	}

	if _, ok := h.LookupResource(ehA); ok {
		t.Error("LookupResource on released slot should return ok=false")
	}
	if _, ok := h.releaseResource(ehA); ok {
		t.Error("releaseResource on already-freed slot should return ok=false")
	}
}

func TestComponentInstance_LookupResource_ZeroHandle(t *testing.T) {
	h := &ComponentInstance{extTable: &externTable{}}
	if _, ok := h.LookupResource(0); ok {
		t.Error("LookupResource(0) should return ok=false")
	}
	if _, ok := h.releaseResource(0); ok {
		t.Error("releaseResource(0) should return ok=false")
	}
}

type dropCallback func(ctx context.Context) error

func (f dropCallback) Drop(ctx context.Context) error { return f(ctx) }

func TestComponentInstance_Close_DropsLIFOAfterCoreClosed(t *testing.T) {
	ctx := context.Background()
	e := core.NewEngine(ctx)
	defer e.Close(ctx)
	b := NewBuilder(e, "closeorder")
	c, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer c.Close(ctx)
	inst, err := c.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}

	errDrop := errors.New("drop failed")
	var order []string
	record := func(name string, dropErr error) dropCallback {
		return func(_ context.Context) error {
			if inst.Core().Poisoned() == nil {
				t.Errorf("Drop(%s) ran while core instance was still usable", name)
			}
			order = append(order, name)
			return dropErr
		}
	}
	inst.RegisterResource(record("first", nil))
	inst.RegisterResource(record("second", errDrop))

	err = inst.Close(ctx)
	if !errors.Is(err, errDrop) {
		t.Errorf("Close() error = %v, want it to wrap %v", err, errDrop)
	}
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Errorf("Drop order = %v, want [second first]", order)
	}
}

func TestComponentInstance_UserState_Immutable(t *testing.T) {
	type state struct{ v int }
	want := &state{v: 42}
	h := &ComponentInstance{userState: want}
	got, ok := h.UserState().(*state)
	if !ok || got != want {
		t.Errorf("UserState() = (%v, %v), want (%p, true)", got, ok, want)
	}
}

func TestWithUserState_PropagatesToWrapper(t *testing.T) {
	ctx := context.Background()
	b, _ := newTestBuilder(t)

	comp, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer comp.Close(ctx)

	type state struct{ v int }
	want := &state{v: 7}
	hi, err := comp.Instantiate(ctx, WithUserState(want))
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer hi.Close(ctx)

	got, ok := hi.UserState().(*state)
	if !ok || got != want {
		t.Errorf("UserState() = (%v, %v), want (%p, true)", got, ok, want)
	}
}

func TestComponentInstance_CoreLookupExtern_RoundTrips(t *testing.T) {
	ctx := context.Background()
	e := core.NewEngine(ctx)
	defer e.Close(ctx)
	b := NewBuilder(e, "extlookup")
	c, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	inst, err := c.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer inst.Close(ctx)

	eh := inst.RegisterResource("payload")
	v, ok := inst.Core().LookupExtern(uint32(eh))
	if !ok || v.(string) != "payload" {
		t.Fatalf("Core().LookupExtern(%d) = (%v, %v), want (\"payload\", true)", eh, v, ok)
	}
}
