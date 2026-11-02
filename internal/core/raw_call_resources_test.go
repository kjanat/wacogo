package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kjanat/wacogo/internal/canon"
	"github.com/kjanat/wazero/api"
)

func rawTestInstance() *ComponentInstance {
	i := &ComponentInstance{canLeave: true}
	i.resources = NewResourceTable(i)
	return i
}

func rawTestFunc(i *ComponentInstance, ft *FuncType, body canon.DirectFunc, post api.Function) *Func {
	return NewFunc(ft, canon.NewCallBinding(FuncTypeParamsAsCanon(ft), FuncTypeResultsAsCanon(ft), canon.Callee{
		Instance: InstanceAsCanon(i), Direct: body, PostReturn: post,
	}))
}

type rawPostReturn struct {
	api.Function
	call func() error
}

func (p rawPostReturn) CallWithStack(context.Context, []uint64) error { return p.call() }

func rawNoop(*CallContext, *CallContext, []uint64) error { return nil }

func TestRawResourcesWriteFailure(t *testing.T) {
	for _, mode := range []string{"error", "panic", "trap"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			caller, callee := rawTestInstance(), rawTestInstance()
			primary := errors.New("later argument failed")
			calls, drops := 0, 0
			tr := &TypeResource{instance: caller, dtor: func(context.Context, uint32) error {
				drops++
				if callee.entered || callee.Poisoned() == nil || !caller.entered {
					t.Error("destructor ran outside cleanup gates")
				}
				return nil
			}}
			own := caller.resources.IssueOwn(tr, 10)
			borrowSource := caller.resources.IssueOwn(tr, 20)
			ft := &FuncType{Params: []ParamType{{Type: TypeOwn{ResourceType: tr}}}}
			fn := rawTestFunc(callee, ft, func(context.Context, []uint64) { calls++ }, nil)
			var task *canon.Task
			var err error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				err = fn.CallRaw(ctx, caller, func(_, cc *CallContext, stack []uint64) error {
					task = cc.Task()
					dst, err := own.TransferOwn(cc.TransferTarget())
					if err != nil {
						return err
					}
					stack[0] = uint64(dst.HandleID())
					if _, err := borrowSource.LendTo(cc.TransferTarget(), cc.Task()); err != nil {
						return err
					}
					switch mode {
					case "panic":
						panic(primary)
					case "trap":
						panic(&canon.Trap{})
					default:
						return primary
					}
				}, rawNoop)
			}()
			if mode == "panic" {
				if panicked != primary {
					t.Fatalf("panic = %v", panicked)
				}
			} else if mode == "trap" {
				var trap *canon.Trap
				if !errors.As(err, &trap) || panicked != nil {
					t.Fatalf("error=%v panic=%v", err, panicked)
				}
			} else if err == nil || !strings.Contains(err.Error(), primary.Error()) || panicked != nil {
				t.Fatalf("error = %v, panic = %v", err, panicked)
			}
			if calls != 0 || drops != 1 || task.NumBorrows != 0 || callee.entered || caller.entered {
				t.Fatalf("calls=%d drops=%d borrows=%d callee=%t caller=%t", calls, drops, task.NumBorrows, callee.entered, caller.entered)
			}
			for _, e := range callee.resources.entries {
				if e.occupied {
					t.Fatal("aborted writer left resource entry")
				}
			}
			if err := borrowSource.Drop(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRawResourcesResultFailureAndCommit(t *testing.T) {
	for _, mode := range []string{"read", "read-panic", "post-return", "task", "success"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			caller, callee := rawTestInstance(), rawTestInstance()
			primary := errors.New(mode + " failed")
			drops, posted := 0, 0
			tr := &TypeResource{instance: caller, dtor: func(context.Context, uint32) error {
				drops++
				if callee.entered {
					t.Error("destructor before callee Exit")
				}
				return nil
			}}
			post := rawPostReturn{call: func() error {
				posted++
				if mode == "post-return" {
					return primary
				}
				return nil
			}}
			ft := &FuncType{Results: []ResultType{{Type: TypeOwn{ResourceType: tr}}}}
			fn := rawTestFunc(callee, ft, func(_ context.Context, stack []uint64) {
				stack[0] = uint64(callee.resources.IssueOwn(tr, 42).HandleID())
			}, post)
			var lifted ResourceHandle
			var panicked any
			var err error
			func() {
				defer func() { panicked = recover() }()
				err = fn.CallRaw(ctx, caller, rawNoop, func(cc, gc *CallContext, stack []uint64) error {
					source, err := gc.LookupOwn(tr, uint32(stack[0]))
					if err != nil {
						return err
					}
					lifted, err = source.TransferOwn(cc.TransferTarget())
					if err != nil {
						return err
					}
					switch mode {
					case "read":
						return primary
					case "read-panic":
						panic(primary)
					case "task":
						cc.Task().AddRelease(func() {
							if !callee.entered {
								t.Error("task release after Exit")
							}
							panic(primary)
						})
					}
					return nil
				})
			}()
			if mode == "success" {
				if err != nil || panicked != nil || drops != 0 || callee.Poisoned() != nil {
					t.Fatalf("success err=%v panic=%v drops=%d", err, panicked, drops)
				}
				if err := lifted.Drop(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "read-panic" {
					if panicked != primary {
						t.Fatalf("panic = %v", panicked)
					}
				} else if !errors.Is(err, primary) || panicked != nil {
					t.Fatalf("err=%v panic=%v", err, panicked)
				}
				if drops != 1 || callee.Poisoned() == nil {
					t.Fatalf("drops=%d poisoned=%v", drops, callee.Poisoned())
				}
			}
			wantPost := 1
			if mode == "read" || mode == "read-panic" {
				wantPost = 0
			}
			if posted != wantPost || callee.entered || caller.entered {
				t.Fatalf("posted=%d entered=%t/%t", posted, callee.entered, caller.entered)
			}
			for _, e := range caller.resources.entries {
				if e.occupied {
					t.Fatal("result cleanup left caller entry")
				}
			}
		})
	}
}

func TestRawResourcesOutstandingBorrowPoisonsAndCleans(t *testing.T) {
	ctx := context.Background()
	caller, callee := rawTestInstance(), rawTestInstance()
	tr := &TypeResource{instance: caller}
	source := caller.resources.IssueOwn(tr, 3)
	ft := &FuncType{Params: []ParamType{{Type: TypeBorrow{ResourceType: tr}}}}
	fn := rawTestFunc(callee, ft, func(context.Context, []uint64) {}, nil)
	var task *canon.Task
	err := fn.CallRaw(ctx, caller, func(_, cc *CallContext, stack []uint64) error {
		task = cc.Task()
		h, err := source.LendTo(cc.TransferTarget(), task)
		if err != nil {
			return err
		}
		stack[0] = uint64(h.HandleID())
		return nil
	}, rawNoop)
	if err == nil || !strings.Contains(err.Error(), "outstanding borrows") || callee.Poisoned() == nil || task.NumBorrows != 0 {
		t.Fatalf("error=%v poison=%v borrows=%d", err, callee.Poisoned(), task.NumBorrows)
	}
	if err := source.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range callee.resources.entries {
		if e.occupied {
			t.Fatal("borrow remained in callee")
		}
	}
}

func TestRawResourcesSlotReuseDoesNotDropUnrelatedEntry(t *testing.T) {
	ctx := context.Background()
	caller, callee := rawTestInstance(), rawTestInstance()
	outside := NewResourceTable(nil)
	drops := 0
	tr := &TypeResource{dtor: func(context.Context, uint32) error { drops++; return nil }}
	source := caller.resources.IssueOwn(tr, 1)
	primary := errors.New("writer failed after alias transfer")
	fn := rawTestFunc(callee, &FuncType{}, func(context.Context, []uint64) { t.Fatal("guest executed") }, nil)
	var retained, replacement ResourceHandle
	err := fn.CallRaw(ctx, caller, func(_, cc *CallContext, _ []uint64) error {
		tracked, err := source.TransferOwn(cc.TransferTarget())
		if err != nil {
			return err
		}
		alias, err := cc.LookupOwn(tr, tracked.HandleID())
		if err != nil {
			return err
		}
		retained, err = alias.TransferOwn(outside)
		if err != nil {
			return err
		}
		// Deliberately bypass the supplied target: this unrelated allocation
		// must not be reclaimed through the old tracked slot's identity.
		replacement = callee.resources.IssueOwn(tr, 2)
		return primary
	}, rawNoop)
	if !errors.Is(err, primary) || drops != 0 || retained.Rep() != 1 || replacement.Rep() != 2 {
		t.Fatalf("err=%v drops=%d", err, drops)
	}
	if err := retained.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Drop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRawResourcesCleanupFailuresPreservePrimary(t *testing.T) {
	for _, mode := range []string{"error", "panic", "primary-panic", "poisoned-definer", "entered-caller"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			caller, callee := rawTestInstance(), rawTestInstance()
			primary, cleanup := errors.New("writer failure"), errors.New("destructor failure")
			drops := 0
			tr := &TypeResource{instance: caller, dtor: func(context.Context, uint32) error {
				drops++
				if callee.entered {
					t.Error("destructor before Exit")
				}
				if mode == "panic" || mode == "primary-panic" {
					panic(cleanup)
				}
				return cleanup
			}}
			if mode == "poisoned-definer" {
				tr.instance = callee
			}
			if mode == "entered-caller" {
				if err := caller.Enter(ctx); err != nil {
					t.Fatal(err)
				}
				defer caller.Exit(ctx)
			}
			first, second := caller.resources.IssueOwn(tr, 1), caller.resources.IssueOwn(tr, 2)
			fn := rawTestFunc(callee, &FuncType{}, func(context.Context, []uint64) { t.Fatal("guest executed") }, nil)
			var err error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				err = fn.CallRaw(ctx, caller, func(_, cc *CallContext, _ []uint64) error {
					if _, err := first.TransferOwn(cc.TransferTarget()); err != nil {
						return err
					}
					if _, err := second.TransferOwn(cc.TransferTarget()); err != nil {
						return err
					}
					if mode == "primary-panic" {
						panic(primary)
					}
					return primary
				}, rawNoop)
			}()
			if mode == "primary-panic" {
				var ok bool
				err, ok = panicked.(error)
				if !ok {
					t.Fatalf("panic=%v", panicked)
				}
			}
			if !errors.Is(err, primary) {
				t.Fatalf("lost primary: %v", err)
			}
			if mode == "poisoned-definer" {
				if drops != 0 || !strings.Contains(err.Error(), "unusable") {
					t.Fatalf("err=%v drops=%d", err, drops)
				}
			} else if drops != 2 || !errors.Is(err, cleanup) {
				t.Fatalf("err=%v drops=%d", err, drops)
			}
			for _, e := range callee.resources.entries {
				if e.occupied {
					t.Fatal("cleanup failure left own entry")
				}
			}
		})
	}
}

func TestRawResourcesReleasedAliasCannotAccessRecycledSlot(t *testing.T) {
	table := NewResourceTable(nil)
	tr := &TypeResource{}
	original := table.IssueOwn(tr, 1)
	alias, err := table.LookupOwn(tr, original.HandleID())
	if err != nil {
		t.Fatal(err)
	}
	if err := alias.Drop(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := table.IssueOwn(tr, 2)
	defer func() {
		if recover() == nil {
			t.Error("stale alias remained usable")
		}
		if replacement.Rep() != 2 {
			t.Error("stale alias affected replacement")
		}
	}()
	_ = original.Rep()
}

func TestRawResourcesSameDefinerBorrowShortcut(t *testing.T) {
	ctx := context.Background()
	caller, callee := rawTestInstance(), rawTestInstance()
	tr := &TypeResource{instance: callee}
	source := caller.resources.IssueOwn(tr, 77)
	primary := errors.New("write failed after shortcut")
	fn := rawTestFunc(callee, &FuncType{}, func(context.Context, []uint64) { t.Fatal("guest executed") }, nil)
	err := fn.CallRaw(ctx, caller, func(_, cc *CallContext, _ []uint64) error {
		borrow, err := source.LendTo(cc.TransferTarget(), cc.Task())
		if err != nil {
			return err
		}
		if borrow.HandleID() != 77 || cc.Task().NumBorrows != 0 || len(callee.resources.entries) != 0 {
			t.Fatal("scoped target lost same-definer borrow shortcut")
		}
		return primary
	}, rawNoop)
	if !errors.Is(err, primary) {
		t.Fatal(err)
	}
	if err := source.Drop(ctx); err != nil {
		t.Fatalf("shortcut lend was not released: %v", err)
	}
}

func TestRawResourcesDestructorCanCallAnotherBinding(t *testing.T) {
	ctx := context.Background()
	caller, callee, sibling := rawTestInstance(), rawTestInstance(), rawTestInstance()
	innerType := &TypeResource{instance: caller}
	inner := rawTestFunc(sibling, &FuncType{Results: []ResultType{{Type: TypeOwn{ResourceType: innerType}}}},
		func(_ context.Context, stack []uint64) {
			stack[0] = uint64(sibling.resources.IssueOwn(innerType, 90).HandleID())
		}, nil)
	var retained ResourceHandle
	drops := 0
	outerType := &TypeResource{instance: caller, dtor: func(context.Context, uint32) error {
		drops++
		if callee.entered {
			t.Error("outer callee still entered")
		}
		return inner.CallRaw(ctx, caller, rawNoop, func(cc, gc *CallContext, stack []uint64) error {
			source, err := gc.LookupOwn(innerType, uint32(stack[0]))
			if err != nil {
				return err
			}
			retained, err = source.TransferOwn(cc.TransferTarget())
			return err
		})
	}}
	source := caller.resources.IssueOwn(outerType, 1)
	primary := errors.New("outer write failed")
	outer := rawTestFunc(callee, &FuncType{}, func(context.Context, []uint64) { t.Fatal("outer guest executed") }, nil)
	err := outer.CallRaw(ctx, caller, func(_, cc *CallContext, _ []uint64) error {
		if _, err := source.TransferOwn(cc.TransferTarget()); err != nil {
			return err
		}
		return primary
	}, rawNoop)
	if !errors.Is(err, primary) || drops != 1 || retained == nil || retained.Rep() != 90 {
		t.Fatalf("reentrant cleanup: err=%v drops=%d retained=%v", err, drops, retained)
	}
	if err := retained.Drop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRawResourcesCompletedTargetRejectsTransferWithoutConsumingSource(t *testing.T) {
	ctx := context.Background()
	caller, callee := rawTestInstance(), rawTestInstance()
	fn := rawTestFunc(callee, &FuncType{}, func(context.Context, []uint64) {}, nil)
	var retainedTarget TransferTarget
	err := fn.CallRaw(ctx, caller, func(_, cc *CallContext, _ []uint64) error {
		retainedTarget = cc.TransferTarget()
		return nil
	}, rawNoop)
	if err != nil {
		t.Fatal(err)
	}
	source := caller.resources.IssueOwn(&TypeResource{}, 7)
	if _, err := source.TransferOwn(retainedTarget); err == nil {
		t.Fatal("completed target accepted transfer")
	}
	if source.Rep() != 7 {
		t.Fatal("failed transfer consumed source")
	}
	if err := source.Drop(ctx); err != nil {
		t.Fatal(err)
	}
}
