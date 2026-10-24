package host_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
)

func TestNestedCloseDoesNotInvokeGuestDestructor(t *testing.T) {
	ctx := t.Context()
	e := wacogo.NewEngine(ctx)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	var opened, dropped int
	b := e.NewHostBuilder("nested-close-provider")
	r := b.AddResource("r", func(context.Context, *host.ComponentInstance, any) error {
		dropped++
		return nil
	})
	b.AddFunction("open", &host.FuncType{
		Results: []host.ResultDecl{{Type: r.Own()}},
	}, func(_ context.Context, cc *host.CallContext, h *host.ComponentInstance, stack []uint64) error {
		resourceType, ok := h.Core().ExportedType("r").(*wacogo.TypeResource)
		if !ok {
			return fmt.Errorf("provider resource export has type %T", h.Core().ExportedType("r"))
		}
		opened++
		handle := cc.IssueOwnHandle(resourceType, uint32(h.RegisterResource(opened)))
		stack[0] = uint64(handle.HandleID())
		return nil
	})
	providerComponent, err := b.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = providerComponent.Close(context.Background()) })
	provider, err := providerComponent.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	// The child's own resource has a real guest destructor. That destructor
	// would call into its parent, whose acquire function retains a new host
	// resource in the parent's canonical resource table.
	component, err := e.LoadComponent(ctx, bytes.NewReader(nestedCloseWasm(t, `(component
  (import "provider" (instance $provider
    (export "r" (type $r (sub resource)))
    (export "open" (func (result (own $r))))))
  (alias export $provider "open" (func $open))
  (core func $open-lowered (canon lower (func $open)))
  (core module $parent-code
    (import "host" "open" (func $open (result i32)))
    (func (export "acquire") call $open drop))
  (core instance $parent-code (instantiate $parent-code
    (with "host" (instance (export "open" (func $open-lowered))))))
  (func $acquire (canon lift (core func $parent-code "acquire")))
  (export "acquire" (func $acquire))
  (component $child
    (import "acquire" (func $acquire))
    (core func $acquire-lowered (canon lower (func $acquire)))
    (core module $destructor
      (import "parent" "acquire" (func $acquire))
      (func (export "drop") (param i32) call $acquire))
    (core instance $destructor (instantiate $destructor
      (with "parent" (instance (export "acquire" (func $acquire-lowered))))))
    (type $trigger (resource (rep i32) (dtor (core func $destructor "drop"))))
    (core func $new-trigger (canon resource.new $trigger))
    (core module $stash
      (import "resource" "new" (func $new (param i32) (result i32)))
      (func (export "stash") i32.const 1 call $new drop))
    (core instance $stash (instantiate $stash
      (with "resource" (instance (export "new" (func $new-trigger))))))
    (func (export "stash") (canon lift (core func $stash "stash"))))
  (instance $child (instantiate $child (with "acquire" (func $acquire))))
  (export "child" (instance $child)))`)))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := component.Instantiate(ctx, wacogo.WithInstanceImport("provider", provider.Core()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close(context.Background()) })
	child := parent.ExportedInstance("child")
	if child == nil || child.ExportedFunc("stash") == nil {
		t.Fatal("missing child stash export")
	}
	if _, err := child.ExportedFunc("stash").Call(ctx); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || dropped != 0 {
		t.Fatalf("before close: opened=%d dropped=%d; want 0, 0", opened, dropped)
	}
	if err := parent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || dropped != 0 {
		t.Fatalf("after close: opened=%d dropped=%d; want 0, 0", opened, dropped)
	}
	if err := parent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if opened != 0 || dropped != 0 {
		t.Fatalf("after repeated closes: opened=%d dropped=%d; want 0, 0", opened, dropped)
	}
}

func TestExportedChildLifetimeRemainsWithOriginalOwner(t *testing.T) {
	ctx := t.Context()
	e := wacogo.NewEngine(ctx)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	ownerComponent, err := e.LoadComponent(ctx, bytes.NewReader(nestedCloseWasm(t, `(component
  (component $child
    (core module $code (func (export "ping")))
    (core instance $code (instantiate $code))
    (func (export "ping") (canon lift (core func $code "ping"))))
  (instance $child (instantiate $child))
  (export "child" (instance $child)))`)))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := ownerComponent.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	child := owner.ExportedInstance("child")
	if child == nil {
		t.Fatal("missing owned child export")
	}
	borrowerComponent, err := e.LoadComponent(ctx, bytes.NewReader(nestedCloseWasm(t, `(component
  (import "borrowed" (instance $borrowed (export "ping" (func))))
  (export "alias" (instance $borrowed)))`)))
	if err != nil {
		t.Fatal(err)
	}
	borrower, err := borrowerComponent.Instantiate(ctx, wacogo.WithInstanceImport("borrowed", child))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = borrower.Close(context.Background()) })
	alias := borrower.ExportedInstance("alias")
	if alias == nil || alias.ExportedFunc("ping") == nil {
		t.Fatal("missing borrowed instance's ping export")
	}
	if err := borrower.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := alias.ExportedFunc("ping").Call(ctx); err != nil {
		t.Fatalf("borrower closure invalidated original owner's child: %v", err)
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := alias.ExportedFunc("ping").Call(ctx); err == nil {
		t.Fatal("retained/imported child remained callable after original owner closed")
	}
}

func nestedCloseWasm(t *testing.T, wat string) []byte {
	t.Helper()
	cmd := exec.Command("wasm-tools", "parse", "-")
	cmd.Stdin = strings.NewReader(wat)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("wasm-tools parse: %v: %s", err, stderr.String())
	}
	return data
}
