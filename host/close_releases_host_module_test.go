package host_test

import (
	"context"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/core"
)

func TestClose_ReleasesInstance(t *testing.T) {
	ctx := context.Background()
	e := wacogo.NewEngine(ctx)
	defer e.Close(ctx)

	b := e.NewHostBuilder("release-host")
	b.AddFunction("noop", &host.FuncType{}, func(context.Context, *core.CallContext, *host.ComponentInstance, []uint64) error {
		return nil
	})
	comp, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer comp.Close(ctx)

	instantiateAndClose := func() *host.ComponentInstance {
		inst, err := comp.Instantiate(ctx)
		if err != nil {
			t.Fatalf("Instantiate: %v", err)
		}
		if err := inst.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		return inst
	}

	first := weak.Make(instantiateAndClose())
	for range 3 {
		instantiateAndClose()
	}
	for i := 0; i < 20 && first.Value() != nil; i++ {
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
	if first.Value() != nil {
		t.Fatal("closed instance is still reachable while its engine is open")
	}
}
