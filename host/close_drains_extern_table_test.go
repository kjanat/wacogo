package host_test

import (
	"context"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/core"
)

// TestClose_DrainsOutstandingResourcesViaExternTable verifies that
// closing an instance with outstanding own-kind canon handles does not
// invoke the registered resource dtor, and that the leftover Go objects
// are instead released through the extern-table drain in LIFO order.
func TestClose_DrainsOutstandingResourcesViaExternTable(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	var dtorCalls int
	var dropOrder []int

	b := e.NewHostBuilder("close-drain-host")
	b.AddResource("r", func(context.Context, *host.ComponentInstance, any) error {
		dtorCalls++
		return nil
	})

	// stash mints an own<r> but never returns it, so the canon slot
	// stays live past the host fn return.
	b.AddFunction("stash", &host.FuncType{
		Params: []host.Param{{Name: "id", Type: host.S32}},
	}, func(_ context.Context, cc *core.CallContext, h *host.ComponentInstance, stack []uint64) error {
		obj := &orderedDrop{id: int(int32(stack[0])), order: &dropOrder}
		eh := h.RegisterResource(obj)
		tr := h.Core().ExportedType("r").(*core.TypeResource)
		_ = cc.IssueOwnHandle(tr, uint32(eh))
		return nil
	})

	comp, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer comp.Close(ctx)
	inst, err := comp.Instantiate(ctx)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}

	for _, id := range []int32{1, 2, 3} {
		if _, err := inst.Core().ExportedFunc("stash").Call(ctx, core.ValS32(id)); err != nil {
			t.Fatalf("stash(%d): %v", id, err)
		}
	}

	if err := inst.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if dtorCalls != 0 {
		t.Fatalf("dtor calls = %d, want 0", dtorCalls)
	}
	want := []int{3, 2, 1}
	if len(dropOrder) != len(want) {
		t.Fatalf("drop order = %v, want %v", dropOrder, want)
	}
	for i, id := range want {
		if dropOrder[i] != id {
			t.Fatalf("drop order = %v, want %v", dropOrder, want)
		}
	}
}

type orderedDrop struct {
	id    int
	order *[]int
}

func (d *orderedDrop) Drop(context.Context) error {
	*d.order = append(*d.order, d.id)
	return nil
}
