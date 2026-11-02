package host

import (
	"context"
	"fmt"

	"github.com/kjanat/wacogo/internal/wasm"
	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
	"github.com/kjanat/wazero/experimental"
)

const (
	stubInitialPages uint32 = 1

	// stubBumpInitial is the starting value of the bump allocator.
	// Must be a multiple of the maximum alignment we'll see (8 for
	// u64/f64). The first stubBumpInitial bytes of stub memory are reserved.
	stubBumpInitial int32 = 16
)

// stubHostImportName is the module name baked into the compiled stub
// bytecode for all host-function imports. Per-instance instantiation
// uses experimental.WithImportResolver to map this name to the
// per-instance host module. Fixed-string baking is what lets the stub
// be compiled once at Build time.
const stubHostImportName = "__wacogo_host__"

// compileStubModule synthesises the stub bytecode for comp and compiles
// it via wazero. The stub imports each comp.funcs[i].hostModExport
// (and each non-nil dtor) from a fixed module name (stubHostImportName);
// per-instance instantiation rebinds those imports at InstantiateModule
// time using experimental.WithImportResolver.
func compileStubModule(ctx context.Context, rt wazero.Runtime, comp *Component) (wazero.CompiledModule, error) {
	var mb wasm.ModuleBuilder

	// 1. Imports MUST come first in the function index space.
	importIdxByFunc := make([]uint32, len(comp.allFuncs))
	for i, fr := range comp.allFuncs {
		sig := wasm.FuncSig{
			Params:  fr.flatParams,
			Results: fr.flatResults,
		}
		importIdxByFunc[i] = mb.AddImportFunc(stubHostImportName, fr.hostModExport, sig)
	}

	dtorSig := wasm.FuncSig{Params: []byte{wasm.ValI32}}
	importIdxByDtor := make([]uint32, len(comp.allResources))
	for i, rr := range comp.allResources {
		if rr.hostModExport == "" {
			continue
		}
		importIdxByDtor[i] = mb.AddImportFunc(stubHostImportName, rr.hostModExport, dtorSig)
	}

	// 2. Bump global.
	bumpGlobalIdx := mb.AddGlobal(wasm.ValI32, true /*mutable*/, stubBumpInitial)
	// Exported so that the Go allocator (stubAllocator) shares it.
	mb.AddExportGlobal("bump", bumpGlobalIdx)

	// 3. Memory; export as "memory".
	memIdx := mb.AddMemory(stubInitialPages, 0 /*no max*/)
	mb.AddExportMemory("memory", memIdx)

	// 4. realloc; export as "realloc".
	reallocSig := wasm.FuncSig{
		Params:  []byte{wasm.ValI32, wasm.ValI32, wasm.ValI32, wasm.ValI32},
		Results: []byte{wasm.ValI32},
	}
	reallocBody, reallocLocals := emitReallocBody(bumpGlobalIdx)
	reallocFnIdx := mb.AddFunc(reallocSig, reallocLocals, reallocBody)
	mb.AddExportFunc("realloc", reallocFnIdx)

	// 5. Per declared func: wrap import (with bump reset); export under
	//    the synthetic stub-level name to avoid collisions with nested
	//    scopes that may reuse a user-facing export name.
	for i, fr := range comp.allFuncs {
		sig := wasm.FuncSig{
			Params:  fr.flatParams,
			Results: fr.flatResults,
		}
		wrapperBody := emitResetForwardBody(bumpGlobalIdx, importIdxByFunc[i], sig)
		wrapperIdx := mb.AddFunc(sig, nil, wrapperBody)
		mb.AddExportFunc(fr.stubExportName, wrapperIdx)
	}

	// 6. Per resource dtor: wrap import (no bump reset).
	for i, rr := range comp.allResources {
		if rr.hostModExport == "" {
			continue
		}
		wrapperBody := emitForwardBody(importIdxByDtor[i], dtorSig)
		wrapperIdx := mb.AddFunc(dtorSig, nil, wrapperBody)
		mb.AddExportFunc(rr.hostModExport, wrapperIdx)
	}

	bin := mb.Encode()
	cm, err := rt.CompileModule(ctx, bin)
	if err != nil {
		return nil, fmt.Errorf("wacogo/host: compile stubMod: %w", err)
	}
	return cm, nil
}

// instantiateStubModule instantiates a previously-compiled stub against
// hostMod. Uses experimental.WithImportResolver to map the fixed
// stubHostImportName baked into the bytecode to the supplied hostMod.
// The instance is configured with an empty module name to keep it
// anonymous in the runtime namespace.
func instantiateStubModule(
	ctx context.Context,
	rt wazero.Runtime,
	cm wazero.CompiledModule,
	hostMod api.Module,
) (api.Module, error) {
	resolver := func(name string) api.Module {
		if name == stubHostImportName {
			return hostMod
		}
		return nil
	}
	resolverCtx := experimental.WithImportResolver(ctx, resolver)
	stubInst, err := rt.InstantiateModule(resolverCtx, cm,
		wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("wacogo/host: instantiate stubMod: %w", err)
	}
	return stubInst, nil
}

// emitReallocBody emits the realloc function body.
// Signature: (old_ptr, old_size, align, new_size) -> new_ptr.
// Semantics: bump allocator, per-call reset. Returns an aligned
// pointer of new_size bytes; grows the linear memory as needed via
// memory.grow when the new bump would otherwise overflow current size.
// Traps on address-space overflow or grow failure. If old_size > 0, it
// copies min(old_size, new_size) bytes from old_ptr.
//
// Locals: two i32s — the aligned result pointer and the new bump.
func emitReallocBody(bumpGlobalIdx uint32) ([]byte, []wasm.LocalEntry) {
	var cb wasm.CodeBuilder

	// Params: 0=old_ptr, 1=old_size, 2=align, 3=new_size
	// Locals: 4=result, 5=new_bump
	const (
		localResult  = 4
		localNewBump = 5
	)

	// result = (bump + align - 1) & ~(align - 1)
	cb.GlobalGet(bumpGlobalIdx) // bump
	cb.LocalGet(2)              // align
	cb.I32Add()                 // bump + align
	cb.I32Const(1)              //
	cb.I32Sub()                 // bump + align - 1
	cb.I32Const(0)              //
	cb.LocalGet(2)              // align
	cb.I32Sub()                 // -align
	cb.I32And()                 // (bump + align - 1) & ~(align - 1) when align power of 2
	cb.LocalSet(localResult)    // result = ...

	// new_bump = result + new_size
	cb.LocalGet(localResult)
	cb.LocalGet(3) // new_size
	cb.I32Add()
	cb.LocalSet(localNewBump)

	// Trap if alignment or size addition wrapped past 2^32.
	cb.LocalGet(localResult)
	cb.GlobalGet(bumpGlobalIdx)
	cb.I32LtU()
	cb.LocalGet(localNewBump)
	cb.LocalGet(localResult)
	cb.I32LtU()
	cb.I32Or()
	cb.If(wasm.BlockTypeEmpty)
	cb.Unreachable()
	cb.End()

	// Grow when ceil(new_bump / 64KiB) exceeds memory.size. Keep this
	// in pages: memory.size << 16 wraps to zero at the valid 4 GiB limit.
	emitRequiredPages(&cb, localNewBump)
	cb.MemorySize(0)
	cb.I32GtU()
	cb.If(wasm.BlockTypeEmpty)
	// pages_to_grow = required_pages - current_pages.
	emitRequiredPages(&cb, localNewBump)
	cb.MemorySize(0)
	cb.I32Sub()
	cb.MemoryGrow(0)
	cb.I32Const(-1)
	cb.I32Eq()
	cb.If(wasm.BlockTypeEmpty)
	cb.Unreachable() // address zero is valid; cannot signal failure with it
	cb.End()
	cb.End()

	// Commit bump = new_bump.
	cb.LocalGet(localNewBump)
	cb.GlobalSet(bumpGlobalIdx)

	// If old_size > 0: memory.copy(result, old_ptr, min(old_size, new_size))
	cb.LocalGet(1) // old_size
	cb.I32Const(0)
	cb.I32Ne()
	cb.If(wasm.BlockTypeEmpty)
	cb.LocalGet(localResult) // dst
	cb.LocalGet(0)           // src (old_ptr)
	// compute min(old_size, new_size) via select: if (new < old) new else old
	cb.LocalGet(3)      // new_size
	cb.LocalGet(1)      // old_size
	cb.LocalGet(3)      // new_size
	cb.LocalGet(1)      // old_size
	cb.I32LtU()         // new_size < old_size
	cb.Select()         // (new_size < old_size) ? new_size : old_size  — NOTE operand order
	cb.MemoryCopy(0, 0) // memory.copy dst src n
	cb.End()

	// Return result
	cb.LocalGet(localResult)
	cb.End()

	locals := []wasm.LocalEntry{wasm.NewLocalEntry(2, wasm.ValI32)}
	return cb.Bytes(), locals
}

// emitRequiredPages leaves ceil(localBytes / 65536) on the stack without
// adding 65535 to localBytes, which could overflow near the wasm32 limit.
func emitRequiredPages(cb *wasm.CodeBuilder, localBytes uint32) {
	cb.LocalGet(localBytes)
	cb.I32Const(16)
	cb.I32ShrU()
	cb.LocalGet(localBytes)
	cb.I32Const((1 << 16) - 1)
	cb.I32And()
	cb.I32Eqz()
	cb.I32Eqz()
	cb.I32Add()
}

// emitForwardBody emits a wrapper body that forwards params to the
// imported function without resetting the bump allocator.
func emitForwardBody(importIdx uint32, sig wasm.FuncSig) []byte {
	var cb wasm.CodeBuilder
	for i := range sig.Params {
		cb.LocalGet(uint32(i))
	}
	cb.Call(importIdx)
	cb.End()
	return cb.Bytes()
}

// emitResetForwardBody emits a wrapper body that:
//  1. global.set bumpGlobalIdx (i32.const 0)
//  2. local.get 0..n-1 (forward params)
//  3. call importIdx
//  4. end
func emitResetForwardBody(bumpGlobalIdx, importIdx uint32, sig wasm.FuncSig) []byte {
	var cb wasm.CodeBuilder
	cb.I32Const(stubBumpInitial)
	cb.GlobalSet(bumpGlobalIdx)
	for i := range sig.Params {
		cb.LocalGet(uint32(i))
	}
	cb.Call(importIdx)
	cb.End()
	return cb.Bytes()
}

// stubAllocator is the Go implementation of the stub's realloc and of
// the bump reset its function wrappers do, over the same memory and bump
// global, so that host calls need not enter the stub at all.
type stubAllocator struct {
	mem  api.Memory
	bump api.MutableGlobal
}

func newStubAllocator(stubMod api.Module) (*stubAllocator, error) {
	bump, ok := stubMod.ExportedGlobal("bump").(api.MutableGlobal)
	if !ok {
		return nil, fmt.Errorf("wacogo/host: stub has no mutable bump global")
	}
	return &stubAllocator{mem: stubMod.Memory(), bump: bump}, nil
}

// reset starts a host call, as emitResetForwardBody does.
func (a *stubAllocator) reset() { a.bump.Set(uint64(uint32(stubBumpInitial))) }

// realloc is emitReallocBody in Go: the same bump allocation, growth, and
// copy of the old block, returning an error on overflow or failed growth.
func (a *stubAllocator) realloc(_ context.Context, oldPtr, oldSize, align, newSize uint32) (uint32, error) {
	bump := uint32(a.bump.Get())
	result := (bump + align - 1) & -align
	newBump := result + newSize
	if result < bump || newBump < result {
		return 0, fmt.Errorf("wacogo/host: realloc: %d bytes overflow the address space", newSize)
	}
	currentPages, _ := a.mem.Grow(0)
	if pages := stubPagesToGrow(currentPages, newBump); pages > 0 {
		if _, ok := a.mem.Grow(pages); !ok {
			return 0, fmt.Errorf("wacogo/host: realloc: out of memory")
		}
	}
	a.bump.Set(uint64(newBump))
	if oldSize > 0 {
		n := min(oldSize, newSize)
		old, ok1 := a.mem.Read(oldPtr, n)
		dst, ok2 := a.mem.Read(result, n)
		if !ok1 || !ok2 {
			return 0, fmt.Errorf("wacogo/host: realloc: out of bounds memory access")
		}
		copy(dst, old)
	}
	return result, nil
}

func stubRequiredPages(bytes uint32) uint32 {
	return uint32((uint64(bytes) + (1<<16 - 1)) >> 16)
}

func stubPagesToGrow(currentPages, newBump uint32) uint32 {
	requiredPages := stubRequiredPages(newBump)
	if requiredPages <= currentPages {
		return 0
	}
	return requiredPages - currentPages
}
