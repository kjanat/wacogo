package canon

import (
	"context"
	"errors"
	"fmt"

	"github.com/kjanat/wazero/api"
)

// CallBinding is a Go→component call binding: a compiled gocall plan
// pre-bound to a Callee. Constructed once per exported Func, invoked many
// times.
type CallBinding struct {
	plan       *gocallPlan
	callee     Callee
	side       *transferSide  // precomputed at construction
	postReturn PostReturnFunc // wrapped at construction; nil if callee has no post-return
	core       coreCallable   // callee.Direct or callee.CoreFunc

	// gcc is per-call state reused across Call invocations. Safe because
	// each callee instance enforces single-threaded, non-reentrant access
	// via its reentrance gate. gcc.callee is set once at construction;
	// gcc.Task.NumBorrows is reset at the start of each Call (Task.End
	// drains releases on every path but does not zero NumBorrows).
	gcc gocallContext
}

// NewCallBinding compiles a gocall plan for params/results and captures the
// Callee it will always dispatch against. The transferSide and wrapped
// post-return are precomputed here so each Call avoids those per-invocation
// allocations.
func NewCallBinding(params, results []Type, callee Callee) *CallBinding {
	side := &transferSide{
		Instance:       callee.Instance,
		Memory:         callee.Memory,
		Realloc:        callee.realloc(),
		StringEncoding: callee.StringEncoding,
		ResourceTable:  callee.Instance.ResourceTable(),
	}
	cb := &CallBinding{
		plan:       compileGocallPlan(params, results),
		callee:     callee,
		side:       side,
		postReturn: wrapPostReturn(callee.PostReturn),
		core:       callee.core(),
	}
	cb.gcc.callee = side
	cb.gcc.limits = callee.LiftLimits
	return cb
}

// Callee returns the captured callee descriptor by value. Used by root-side
// code that needs the underlying memory/realloc/CoreFunc (e.g. adapter
// construction that treats this Func as a target in a cross-component call).
func (cb *CallBinding) Callee() Callee { return cb.callee }

// Call executes a Go→component call. Traps raised inside the canonical-ABI
// pipeline are recovered and returned as errors; no *Trap sentinel leaks
// past this boundary.
func (cb *CallBinding) Call(ctx context.Context, args []Val) (results []Val, err error) {
	defer cb.recoverTrap(&err)

	cb.gcc.Task.NumBorrows = 0
	return runGocallPlan(ctx, cb.plan, &cb.gcc, args, cb.core, cb.postReturn)
}

// CallRaw invokes the function using caller-supplied flat-stack closures,
// bypassing the gocall plan's Val lift/lower steps. writeArgs populates the
// stack before the wasm call; readResult reads from it after.
//
// A write error stops the call before guest execution. A read error stops it
// before post-return. Either failure poisons the entered instance.
//
// The reentrance gate and post-return hook are still honoured. No resource
// table or realloc operations are performed — the caller is responsible for
// all encoding.
func (cb *CallBinding) CallRaw(
	ctx context.Context,
	writeArgs func(stack []uint64) error,
	readResult func(stack []uint64) error,
) error {
	return cb.CallRawWithFinalizer(ctx, writeArgs, readResult, nil)
}

// CallRawWithFinalizer additionally ends caller-side resource accounting before
// poison and Exit, including on callback panic. finish must not call the guest.
// It is invoked once for an entered call and never if Enter fails.
func (cb *CallBinding) CallRawWithFinalizer(
	ctx context.Context,
	writeArgs func(stack []uint64) error,
	readResult func(stack []uint64) error,
	finish func() error,
) (err error) {
	defer cb.recoverTrap(&err)

	if err := cb.callee.Instance.Enter(ctx); err != nil {
		return err
	}
	defer exitWithError(ctx, cb.callee.Instance, &err)
	defer poisonOnFailure(cb.callee.Instance, &err)
	if finish != nil {
		defer finalizeRawCall(&err, finish)
	}

	stack := cb.plan.coreStack
	clear(stack)
	if err = func() error {
		prev := cb.callee.Instance.SuspendLeave()
		defer cb.callee.Instance.RestoreLeave(prev)
		return writeArgs(stack)
	}(); err != nil {
		return err
	}

	if err = cb.core.CallWithStack(ctx, stack); err != nil {
		return err
	}

	if err = ctx.Err(); err != nil {
		return err
	}
	if err = cb.preflightRawResults(ctx, stack); err != nil {
		return err
	}
	if err = readResult(stack); err != nil {
		return err
	}
	return cb.finishPostReturn(ctx, stack[:cb.plan.nResultRegs])
}

func finalizeRawCall(result *error, finish func() error) {
	panicked := recover()
	endErr := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				if cause, ok := p.(error); ok {
					err = fmt.Errorf("raw call finalizer: %w", cause)
				} else {
					err = fmt.Errorf("raw call finalizer: %v", p)
				}
			}
		}()
		return finish()
	}()
	*result = errors.Join(*result, endErr)
	if panicked != nil {
		if endErr == nil {
			panic(panicked)
		}
		if cause, ok := panicked.(error); ok {
			panic(errors.Join(cause, *result))
		}
		panic(errors.Join(fmt.Errorf("component call panic: %v", panicked), *result))
	}
}

// recoverTrap preserves cleanup errors joined to a canonical trap. Other Go
// panics retain their normal propagation instead of becoming successful calls.
func (cb *CallBinding) recoverTrap(err *error) {
	if recovered := recover(); recovered != nil {
		cause, ok := recovered.(error)
		var trap *Trap
		if ok && errors.As(cause, &trap) {
			*err = cause
			return
		}
		panic(recovered)
	}
}

func (cb *CallBinding) finishPostReturn(ctx context.Context, stack []uint64) error {
	if cb.postReturn == nil {
		return nil
	}
	prev := cb.callee.Instance.SuspendLeave()
	defer cb.callee.Instance.RestoreLeave(prev)
	return cb.postReturn(ctx, stack)
}

// poisonOnFailure runs before releasing the instance's entry gate. A failed
// canonical conversion cannot leave a partially mutated instance callable.
// err is nil for transfer callbacks, which report failures only through traps.
func poisonOnFailure(instance Instance, err *error) {
	if recovered := recover(); recovered != nil {
		cause, ok := recovered.(error)
		if !ok {
			cause = fmt.Errorf("canonical call panic: %v", recovered)
		}
		instance.Poison(cause)
		panic(recovered)
	}
	if err != nil && *err != nil {
		instance.Poison(*err)
	}
}

// preflightRawResults visits the complete result before a generated or custom
// decoder can allocate. It shares Call's compiled layout but never transfers
// resources or materializes collections. The budget is local to this invocation.
func (cb *CallBinding) preflightRawResults(ctx context.Context, stack []uint64) error {
	gcc := gocallContext{
		callee: cb.side, registers: stack, validateOnly: true, generatedStorage: true,
		lift: liftBudget{limits: cb.callee.LiftLimits},
	}
	var base uint32
	if cb.plan.returnMem {
		base = uint32(stack[0])
		if err := checkAlignment(base, cb.plan.resultMaxAlign); err != nil {
			return err
		}
	}
	for _, step := range cb.plan.resultSteps {
		if _, err := step(ctx, &gcc, base); err != nil {
			return err
		}
	}
	return nil
}

// wrapRealloc adapts an api.Function realloc into ReallocFunc, or nil.
// The returned closure uses CallWithStack with a pre-allocated stack so
// every realloc invocation is allocation-free. Safe under the per-instance
// single-threaded invariant — the stack is shared across invocations of
// this closure but never concurrent.
func wrapRealloc(f api.Function) ReallocFunc {
	if f == nil {
		return nil
	}
	// Realloc sig: (i32 origPtr, i32 origSize, i32 align, i32 newSize) -> i32.
	// CallWithStack stack length must be max(numParams, numResults) = 4.
	stack := make([]uint64, 4)
	return func(ctx context.Context, origPtr, origSize, align, newSize uint32) (uint32, error) {
		stack[0] = uint64(origPtr)
		stack[1] = uint64(origSize)
		stack[2] = uint64(align)
		stack[3] = uint64(newSize)
		if err := f.CallWithStack(ctx, stack); err != nil {
			return 0, err
		}
		return uint32(stack[0]), nil
	}
}

// wrapPostReturn adapts an api.Function post-return into PostReturnFunc, or nil.
// Uses CallWithStack so the call is allocation-free; the caller-supplied
// results slice doubles as the wazero call stack (post-return params are
// the function's flat result types, no results returned).
func wrapPostReturn(f api.Function) PostReturnFunc {
	if f == nil {
		return nil
	}
	return func(ctx context.Context, results []uint64) error {
		return f.CallWithStack(ctx, results)
	}
}
