package host

import (
	"context"
	"errors"
	"testing"

	"github.com/kjanat/wacogo/internal/core"
	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
)

func TestBuildPerInstanceHostMod_ClosesCompiledCodeWhenInstantiationFails(t *testing.T) {
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

	instantiateErr := errors.New("forced host module instantiation failure")
	rt := &failingHostInstantiationRuntime{
		Runtime: core.WazeroRuntime(e),
		err:     instantiateErr,
	}
	_, _, err = buildPerInstanceHostMod(ctx, rt, comp, &ComponentInstance{})
	if !errors.Is(err, instantiateErr) {
		t.Fatalf("buildPerInstanceHostMod error = %v, want wrapped %v", err, instantiateErr)
	}
	if rt.compiled == nil {
		t.Fatal("host-module builder did not compile")
	}
	if !rt.compiled.closed {
		t.Fatal("compiled host code was not closed after instantiation failure")
	}
}

type failingHostInstantiationRuntime struct {
	wazero.Runtime
	err      error
	compiled *closeTrackingCompiledModule
}

func (r *failingHostInstantiationRuntime) NewHostModuleBuilder(name string) wazero.HostModuleBuilder {
	return &closeTrackingHostModuleBuilder{
		HostModuleBuilder: r.Runtime.NewHostModuleBuilder(name),
		compiled:          &r.compiled,
	}
}

func (r *failingHostInstantiationRuntime) InstantiateModule(context.Context, wazero.CompiledModule, wazero.ModuleConfig) (api.Module, error) {
	return nil, r.err
}

type closeTrackingHostModuleBuilder struct {
	wazero.HostModuleBuilder
	compiled **closeTrackingCompiledModule
}

func (b *closeTrackingHostModuleBuilder) Compile(ctx context.Context) (wazero.CompiledModule, error) {
	compiled, err := b.HostModuleBuilder.Compile(ctx)
	if err != nil {
		return nil, err
	}
	tracked := &closeTrackingCompiledModule{CompiledModule: compiled}
	*b.compiled = tracked
	return tracked, nil
}

type closeTrackingCompiledModule struct {
	wazero.CompiledModule
	closed bool
}

func (m *closeTrackingCompiledModule) Close(ctx context.Context) error {
	err := m.CompiledModule.Close(ctx)
	m.closed = true
	m.CompiledModule = nil
	return err
}
