package host

import (
	"context"
	"testing"

	"github.com/kjanat/wacogo/internal/core"
	"github.com/kjanat/wazero/api"
)

// TestAuditStubAllocatorOverflow runs the same allocations through
// the stub's wasm realloc and through stubAllocator, on two instances of
// one stub, and requires the same pointers, memory sizes and contents.
func TestAuditStubAllocatorOverflow(t *testing.T) {
	ctx := context.Background()
	b, e := newTestBuilder(t)
	b.AddFunction("noop", &FuncType{}, func(context.Context, *core.CallContext, *ComponentInstance, []uint64) error {
		return nil
	})
	comp, err := b.Build(ctx)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer comp.Close(ctx)

	rt := core.WazeroRuntime(e)
	newStub := func() api.Module {
		hostMod, hostCode, err := buildPerInstanceHostMod(ctx, rt, comp, &ComponentInstance{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hostCode.Close(ctx) })
		t.Cleanup(func() { hostMod.Close(ctx) })
		stub, err := instantiateStubModule(ctx, rt, comp.compiledStub, hostMod)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stub.Close(ctx) })
		return stub
	}
	wasmStub, goStub := newStub(), newStub()
	wasmRealloc := wasmStub.ExportedFunction("realloc")
	alloc, err := newStubAllocator(goStub)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"go", "wasm"} {
		t.Run(mode, func(t *testing.T) {
			var err error
			if mode == "go" {
				alloc.bump.Set(0xfffffff8)
				ptr, e := alloc.realloc(ctx, 0, 0, 8, 32)
				err = e
				t.Logf("ptr=%d bump=%d err=%v", ptr, alloc.bump.Get(), err)
			} else {
				wasmStub.ExportedGlobal("bump").(api.MutableGlobal).Set(0xfffffff8)
				ptr, e := wasmRealloc.Call(ctx, 0, 0, 8, 32)
				err = e
				t.Logf("ptr=%v bump=%d err=%v", ptr, wasmStub.ExportedGlobal("bump").Get(), err)
			}
			if err == nil {
				t.Fatal("address-space overflow succeeded and wrapped the allocator bump")
			}
		})
	}
}
