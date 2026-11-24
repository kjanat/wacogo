package host

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/kjanat/wacogo/internal/core"
)

func TestNestedInstanceTypeKeepsDeclarationOrder(t *testing.T) {
	b, _ := newTestBuilder(t)
	noop := func(context.Context, *core.CallContext, *ComponentInstance, []uint64) error { return nil }
	nested := b.AddNestedInstance("nested")
	nested.AddFunction("zeta", &FuncType{}, noop)
	nested.AddFunction("alpha", &FuncType{}, noop)
	nested.AddFunction("mid", &FuncType{}, noop)
	comp, err := b.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = comp.Close(context.Background()) })

	var found bool
	for _, it := range comp.arena.InstanceTypes {
		if !slices.Equal(slices.Sorted(maps.Keys(it.Exports)), []string{"alpha", "mid", "zeta"}) {
			continue
		}
		found = true
		if want := []string{"zeta", "alpha", "mid"}; !slices.Equal(it.ExportOrder, want) {
			t.Fatalf("ExportOrder = %q, want %q", it.ExportOrder, want)
		}
	}
	if !found {
		t.Fatal("nested instance type not pushed")
	}
}
