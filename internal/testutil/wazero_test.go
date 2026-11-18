package testutil

import (
	"errors"
	"testing"

	"github.com/kjanat/wazero"
)

func TestChainedHostBuilderTracksCompile(t *testing.T) {
	ctx := t.Context()
	rt := &Runtime{Runtime: wazero.NewRuntime(ctx)}
	defer rt.Close(ctx)
	b := rt.NewHostModuleBuilder("host").NewFunctionBuilder().
		WithFunc(func(x uint32) uint32 { return x }).WithName("identity").
		WithParameterNames("x").WithResultNames("y").Export("id")
	fault := errors.New("stop before compile")
	rt.BeforeHostCompile = func() error { return fault }
	if _, err := b.Compile(ctx); !errors.Is(err, fault) {
		t.Fatalf("chained Compile did not reach the hook: %v", err)
	}
	rt.BeforeHostCompile = nil
	code, err := b.Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if code.ExportedFunctions()["id"] == nil || rt.LiveHost.Load() != 1 {
		t.Fatal("chained Compile did not track the exported host function")
	}
	if err := code.Close(ctx); err != nil || rt.LiveHost.Load() != 0 {
		t.Fatalf("closing chained compile: live=%d, err=%v", rt.LiveHost.Load(), err)
	}
}

func TestChainedHostBuilderRejectsDirectInstantiate(t *testing.T) {
	ctx := t.Context()
	rt := &Runtime{Runtime: wazero.NewRuntime(ctx)}
	defer rt.Close(ctx)

	builder := rt.NewHostModuleBuilder("host").NewFunctionBuilder().
		WithFunc(func() {}).Export("f")
	mod, err := builder.Instantiate(ctx)
	if err == nil || mod != nil || rt.Module("host") != nil {
		t.Fatalf("direct Instantiate must fail before creating a module: module=%v, err=%v", mod, err)
	}
}
