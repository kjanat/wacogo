package core

import (
	"context"
	"strings"
	"testing"

	"github.com/kjanat/wacogo/internal/canon"
)

func TestAdapterResourceEffectsBeforeInvalidArgument(t *testing.T) {
	for _, tc := range []struct {
		name       string
		own        bool
		sameOwner  bool
		wantPoison bool
	}{
		{name: "own transfer", own: true, wantPoison: true},
		{name: "cross-instance borrow", wantPoison: true},
		{name: "defining-instance borrow", sameOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			engine := NewEngine(ctx)
			defer engine.Close(ctx)
			caller, callee, definer := rawTestInstance(), rawTestInstance(), rawTestInstance()
			if tc.sameOwner {
				definer = callee
			}
			var dropped []uint32
			resource := &TypeResource{instance: definer, dtor: func(_ context.Context, rep uint32) error {
				dropped = append(dropped, rep)
				return nil
			}}
			var resourceParam Type = TypeBorrow{ResourceType: resource}
			if tc.own {
				resourceParam = TypeOwn{ResourceType: resource}
			}
			source := caller.resources.IssueOwn(resource, 77)
			id := source.HandleID()
			binding, err := engine.canonHost.BuildAdapter(ctx, canon.CallSide{Instance: canonInstanceView{caller}}, canon.Callee{
				Instance: canonInstanceView{callee},
				Direct: func(context.Context, []uint64) {
					t.Error("callee ran before all arguments were validated")
				},
			}, FuncTypeParamsAsCanon(&FuncType{Params: []ParamType{
				{Type: resourceParam}, {Type: TypeChar{}},
			}}), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer binding.Close(ctx)
			stack := []uint64{uint64(id), 0x110000}
			var trapped any
			func() {
				defer func() { trapped = recover() }()
				binding.Func.Call(ctx, nil, stack)
			}()
			if trap, ok := trapped.(*canon.Trap); !ok || !strings.Contains(trap.Error(), "char") {
				t.Fatalf("invalid second argument trapped with %v", trapped)
			}
			err = callee.Enter(ctx)
			if err == nil {
				callee.Exit(ctx)
			}
			if (err != nil) != tc.wantPoison {
				t.Fatalf("callee availability after failed transfer: %v", err)
			}
			if len(dropped) != 0 {
				t.Fatalf("failed argument validation ran destructors: %v", dropped)
			}
			switch {
			case tc.own:
				if _, err := caller.resources.LookupOwn(resource, id); err == nil {
					t.Fatal("own transfer left a second owner in the caller")
				}
				dest, err := callee.resources.LookupOwn(resource, uint32(stack[0]))
				if err != nil || dest.Rep() != 77 {
					t.Fatalf("own transfer did not retain the resource in its destination: %v", err)
				}
				if err := dest.Drop(ctx); err != nil {
					t.Fatal(err)
				}
			case tc.sameOwner:
				if len(callee.resources.entries) != 0 || stack[0] != 77 {
					t.Fatal("defining-instance borrow allocated a destination entry")
				}
			default:
				borrow, err := callee.resources.LookupBorrowable(resource, uint32(stack[0]))
				if err != nil || borrow.Rep() != 77 {
					t.Fatalf("cross-instance borrow created no destination entry: %v", err)
				}
				if err := borrow.Drop(ctx); err != nil || len(dropped) != 0 {
					t.Fatalf("dropping the borrow ran a destructor: %v, %v", dropped, err)
				}
			}
			if !tc.own {
				slot := &caller.resources.entries[id-1]
				if slot.numLends != 0 {
					t.Fatalf("unwinding retained %d source lends", slot.numLends)
				}
				if err := source.Drop(ctx); err != nil {
					t.Fatalf("source could not be dropped after unwind: %v", err)
				}
			}
			if len(dropped) != 1 || dropped[0] != 77 {
				t.Fatalf("destructor calls = %v, want [77]", dropped)
			}
		})
	}
}
