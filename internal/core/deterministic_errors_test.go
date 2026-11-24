package core

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestInstantiateNamesFirstNilImport(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)
	comp, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, `(component
  (import "h" (instance)) (import "g" (instance)) (import "f" (instance)) (import "e" (instance))
  (import "d" (instance)) (import "c" (instance)) (import "b" (instance)) (import "a" (instance)))`)))
	if err != nil {
		t.Fatal(err)
	}
	defer comp.Close(ctx)
	var opts []InstantiateOption
	for _, name := range []string{"h", "g", "f", "e", "d", "c", "b", "a"} {
		opts = append(opts, WithInstanceImport(name, nil))
	}
	want := `wacogo: instantiate: import "a": no instance supplied`
	for range 32 {
		if _, err := comp.Instantiate(ctx, opts...); err == nil || err.Error() != want {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
}

func TestOuterClosurePanicsNameFirstSlot(t *testing.T) {
	closures := make(map[int]outerRef)
	for slot := range 8 {
		closures[slot] = outerRef{count: 1, index: uint32(10 + slot)}
	}
	engine := NewEngine(t.Context())
	defer engine.Close(t.Context())
	for name, c := range map[string]struct {
		run  func()
		want string
	}{
		"instantiate module": {
			run: func() {
				_, _ = engine.instantiateWithParentCfg(t.Context(), &Component{outerModuleClosures: closures}, &instantiationState{}, nil, &instantiateConfig{})
			},
			want: "wacogo: outer closure module index 10 out of range (have 0)",
		},
		"instantiate component": {
			run: func() {
				_, _ = engine.instantiateWithParentCfg(t.Context(), &Component{outerComponentClosures: closures}, &instantiationState{}, nil, &instantiateConfig{})
			},
			want: "wacogo: outer closure component index 10 out of range (have 0)",
		},
		"resolve module": {
			run: func() {
				s := &instantiationState{subComponents: []*Component{{outerModuleClosures: closures}}}
				s.resolveSubComponentClosures()
			},
			want: "wacogo: closure resolution module index 10 out of range (have 0)",
		},
		"resolve component": {
			run: func() {
				s := &instantiationState{subComponents: []*Component{{outerComponentClosures: closures}}}
				s.resolveSubComponentClosures()
			},
			want: "wacogo: closure resolution component index 10 out of range (have 1)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			for range 32 {
				got := func() (p any) {
					defer func() { p = recover() }()
					c.run()
					return nil
				}()
				if got != c.want {
					t.Fatalf("want panic %q, got %v", c.want, got)
				}
			}
		})
	}
}

func TestDeferredTableRefundErrorsFollowModuleOrder(t *testing.T) {
	data := buildComponentBytes(t, `(component
      (core module $m (table 0 2 funcref))
      (core instance (instantiate $m))
      (core instance (instantiate $m))
      (core instance (instantiate $m))
      (core instance (instantiate $m)))`)
	want := "core table release: release 3\ncore table release: release 2\ncore table release: release 1\ncore table release: release 0"
	for range 16 {
		engine := NewEngine(t.Context())
		allocated := 0
		ctx := WithCoreTableAllocator(t.Context(), func([]CoreTable) (func(), error) {
			failure := fmt.Errorf("release %d", allocated)
			allocated++
			return func() { panic(failure) }, nil
		})
		component, err := engine.LoadComponent(ctx, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		instance, err := component.Instantiate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = instance.RunInComponent(ctx, func() error { return engine.Close(ctx) })
		if err == nil || err.Error() != want {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
}
