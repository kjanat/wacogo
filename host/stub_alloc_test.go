package host

import (
	"bytes"
	"context"
	"testing"

	"github.com/kjanat/wacogo/internal/core"
	"github.com/kjanat/wacogo/internal/wasm"
	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
)

func newOnePageStub(t *testing.T) (context.Context, api.Module) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	var mb wasm.ModuleBuilder
	bumpIdx := mb.AddGlobal(wasm.ValI32, true, stubBumpInitial)
	mb.AddExportGlobal("bump", bumpIdx)
	memIdx := mb.AddMemory(1, 1)
	mb.AddExportMemory("memory", memIdx)
	sig := wasm.FuncSig{
		Params:  []byte{wasm.ValI32, wasm.ValI32, wasm.ValI32, wasm.ValI32},
		Results: []byte{wasm.ValI32},
	}
	body, locals := emitReallocBody(bumpIdx)
	fnIdx := mb.AddFunc(sig, locals, body)
	mb.AddExportFunc("realloc", fnIdx)
	compiled, err := rt.CompileModule(ctx, mb.Encode())
	if err != nil {
		t.Fatalf("compile one-page stub: %v", err)
	}
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("one_page_stub"))
	if err != nil {
		t.Fatalf("instantiate one-page stub: %v", err)
	}
	t.Cleanup(func() { _ = mod.Close(ctx) })
	return ctx, mod
}

// TestStubAllocator_MatchesWasmRealloc runs the same allocations through
// the stub's wasm realloc and through stubAllocator, on two instances of
// one stub, and requires the same pointers, memory sizes and contents.
func TestStubAllocator_MatchesWasmRealloc(t *testing.T) {
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

	type op struct {
		reset                        bool
		oldPtr, oldSize, align, size uint32
	}
	ops := []op{
		{align: 1, size: 3},
		{align: 8, size: 16},
		{align: 4, size: 0},
		{align: 2, size: 100},
		{align: 1, size: 70000}, // grows the memory
		{reset: true},
		{align: 8, size: 24},
		{oldPtr: 16, oldSize: 24, align: 8, size: 48}, // grow in place of the last block: copies
		{oldPtr: 16, oldSize: 48, align: 4, size: 8},  // shrink: copies the prefix
		{align: 1, size: 3 << 16},                     // grows by several pages
	}
	var bumpW api.MutableGlobal = wasmStub.ExportedGlobal("bump").(api.MutableGlobal)
	for i, o := range ops {
		if o.reset {
			bumpW.Set(uint64(uint32(stubBumpInitial)))
			alloc.reset()
			continue
		}
		// Distinct contents at the source, so copies are checked.
		for _, m := range []api.Memory{wasmStub.Memory(), goStub.Memory()} {
			if o.oldSize > 0 {
				src, _ := m.Read(o.oldPtr, o.oldSize)
				for j := range src {
					src[j] = byte(i + j)
				}
			}
		}
		res, err := wasmRealloc.Call(ctx, uint64(o.oldPtr), uint64(o.oldSize), uint64(o.align), uint64(o.size))
		if err != nil {
			t.Fatalf("op %d: wasm realloc: %v", i, err)
		}
		got, err := alloc.realloc(ctx, o.oldPtr, o.oldSize, o.align, o.size)
		if err != nil {
			t.Fatalf("op %d: Go realloc: %v", i, err)
		}
		if want := uint32(res[0]); got != want {
			t.Fatalf("op %d %+v: Go realloc = %d, wasm = %d", i, o, got, want)
		}
		if gw, gg := bumpW.Get(), alloc.bump.Get(); gw != gg {
			t.Fatalf("op %d: bump: Go %d, wasm %d", i, gg, gw)
		}
		mw, mg := wasmStub.Memory(), goStub.Memory()
		if mw.Size() != mg.Size() {
			t.Fatalf("op %d: memory size: Go %d, wasm %d", i, mg.Size(), mw.Size())
		}
		bw, _ := mw.Read(0, mw.Size())
		bg, _ := mg.Read(0, mg.Size())
		if !bytes.Equal(bw, bg) {
			t.Fatalf("op %d: memory contents differ", i)
		}
	}

	for _, tc := range []struct {
		name  string
		bump  uint32
		align uint32
		size  uint32
	}{
		{name: "alignment overflow", bump: 0xfffffffc, align: 8, size: 1},
		{name: "size overflow", bump: 0xfffffff8, align: 8, size: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bumpW.Set(uint64(tc.bump))
			alloc.bump.Set(uint64(tc.bump))
			if _, err := wasmRealloc.Call(ctx, 0, 0, uint64(tc.align), uint64(tc.size)); err == nil {
				t.Fatal("wasm realloc succeeded, want an overflow trap")
			}
			if _, err := alloc.realloc(ctx, 0, 0, tc.align, tc.size); err == nil {
				t.Fatal("Go realloc succeeded, want an overflow error")
			}
			if got := bumpW.Get(); got != uint64(tc.bump) {
				t.Fatalf("wasm bump = %d, want unchanged %d", got, tc.bump)
			}
			if got := alloc.bump.Get(); got != uint64(tc.bump) {
				t.Fatalf("Go bump = %d, want unchanged %d", got, tc.bump)
			}
		})
	}
}

func TestStubAllocatorGrowFailureLeavesBumpUnchanged(t *testing.T) {
	ctx, mod := newOnePageStub(t)
	alloc, err := newStubAllocator(mod)
	if err != nil {
		t.Fatal(err)
	}
	bump := alloc.bump.Get()

	if _, err := mod.ExportedFunction("realloc").Call(ctx, 0, 0, 1, 1<<16); err == nil {
		t.Fatal("wasm realloc succeeded despite memory maximum")
	}
	if got := alloc.bump.Get(); got != bump {
		t.Fatalf("wasm bump = %d after failed growth, want %d", got, bump)
	}

	if _, err := alloc.realloc(ctx, 0, 0, 1, 1<<16); err == nil {
		t.Fatal("Go realloc succeeded despite memory maximum")
	}
	if got := alloc.bump.Get(); got != bump {
		t.Fatalf("Go bump = %d after failed growth, want %d", got, bump)
	}
}

func TestStubRequiredPagesAtWasm32Limit(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	var mb wasm.ModuleBuilder
	sig := wasm.FuncSig{Params: []byte{wasm.ValI32}, Results: []byte{wasm.ValI32}}
	var cb wasm.CodeBuilder
	emitRequiredPages(&cb, 0)
	cb.End()
	fnIdx := mb.AddFunc(sig, nil, cb.Bytes())
	mb.AddExportFunc("required_pages", fnIdx)
	compiled, err := rt.CompileModule(ctx, mb.Encode())
	if err != nil {
		t.Fatalf("compile page-count helper: %v", err)
	}
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName("required_pages"))
	if err != nil {
		t.Fatalf("instantiate page-count helper: %v", err)
	}
	t.Cleanup(func() { _ = mod.Close(ctx) })

	for _, tc := range []struct {
		bytes uint32
		pages uint32
	}{
		{bytes: 0, pages: 0},
		{bytes: 1, pages: 1},
		{bytes: 65535, pages: 1},
		{bytes: 65536, pages: 1},
		{bytes: 65537, pages: 2},
		{bytes: ^uint32(0), pages: 65536},
	} {
		goPages := stubRequiredPages(tc.bytes)
		res, err := mod.ExportedFunction("required_pages").Call(ctx, uint64(tc.bytes))
		if err != nil {
			t.Fatalf("wasm required_pages(%d): %v", tc.bytes, err)
		}
		if goPages != tc.pages || uint32(res[0]) != tc.pages {
			t.Errorf("required pages for %d bytes: Go=%d wasm=%d, want %d", tc.bytes, goPages, res[0], tc.pages)
		}
	}

	for _, tc := range []struct {
		current uint32
		bytes   uint32
		grow    uint32
	}{
		{current: 65536, bytes: ^uint32(0), grow: 0},
		{current: 65535, bytes: ^uint32(0), grow: 1},
		{current: 1, bytes: 65536, grow: 0},
		{current: 1, bytes: 65537, grow: 1},
	} {
		if got := stubPagesToGrow(tc.current, tc.bytes); got != tc.grow {
			t.Errorf("pages to grow from %d for %d bytes = %d, want %d", tc.current, tc.bytes, got, tc.grow)
		}
	}
}
