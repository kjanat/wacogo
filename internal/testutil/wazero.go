// Package testutil provides helpers shared by runtime tests.
package testutil

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/kjanat/wazero"
	"github.com/kjanat/wazero/api"
)

// Runtime tracks compiled handles and actual module instances. Configure the
// hooks before starting concurrent operations. Modules themselves stay unwrapped
// because wazero's import resolver requires their concrete implementation.
type Runtime struct {
	wazero.Runtime
	AfterCompile                   func(context.Context, *Code)
	BeforeInstantiate              func(context.Context, *Code) error
	BeforeHostCompile              func() error
	LiveWasm, LiveHost, HostBuilds atomic.Int64

	mu      sync.Mutex
	modules []api.Module
}

func (r *Runtime) CompileModule(ctx context.Context, binary []byte) (wazero.CompiledModule, error) {
	code, err := r.Runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, err
	}
	return r.track(ctx, code, false), nil
}

func (r *Runtime) track(ctx context.Context, code wazero.CompiledModule, host bool) *Code {
	live := &r.LiveWasm
	if host {
		live = &r.LiveHost
	}
	live.Add(1)
	tracked := &Code{CompiledModule: code, Host: host, live: live}
	if r.AfterCompile != nil {
		r.AfterCompile(ctx, tracked)
	}
	return tracked
}

func (r *Runtime) InstantiateModule(ctx context.Context, code wazero.CompiledModule, config wazero.ModuleConfig) (api.Module, error) {
	if tracked, ok := code.(*Code); ok {
		if r.BeforeInstantiate != nil {
			if err := r.BeforeInstantiate(ctx, tracked); err != nil {
				return nil, err
			}
		}
		code = tracked.CompiledModule
	}
	mod, err := r.Runtime.InstantiateModule(ctx, code, config)
	if err == nil {
		r.mu.Lock()
		r.modules = append(r.modules, mod)
		r.mu.Unlock()
	}
	return mod, err
}

func (r *Runtime) Modules() []api.Module {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.Module(nil), r.modules...)
}

func (r *Runtime) NewHostModuleBuilder(name string) wazero.HostModuleBuilder {
	r.HostBuilds.Add(1)
	return &hostBuilder{HostModuleBuilder: r.Runtime.NewHostModuleBuilder(name), runtime: r}
}

type hostBuilder struct {
	wazero.HostModuleBuilder
	runtime *Runtime
}

// Each fluent call must return the wrapper: handing the embedded builder
// back to the caller would silently bypass tracking on the final Compile.
func (b *hostBuilder) NewFunctionBuilder() wazero.HostFunctionBuilder {
	return &hostFunctionBuilder{function: b.HostModuleBuilder.NewFunctionBuilder(), module: b}
}

type hostFunctionBuilder struct {
	function wazero.HostFunctionBuilder
	module   *hostBuilder
}

func (f *hostFunctionBuilder) WithFunc(fn any) wazero.HostFunctionBuilder {
	f.function = f.function.WithFunc(fn)
	return f
}

func (f *hostFunctionBuilder) WithGoFunction(fn api.GoFunction, params, results []api.ValueType) wazero.HostFunctionBuilder {
	f.function = f.function.WithGoFunction(fn, params, results)
	return f
}

func (f *hostFunctionBuilder) WithGoModuleFunction(fn api.GoModuleFunction, params, results []api.ValueType) wazero.HostFunctionBuilder {
	f.function = f.function.WithGoModuleFunction(fn, params, results)
	return f
}

func (f *hostFunctionBuilder) WithName(name string) wazero.HostFunctionBuilder {
	f.function = f.function.WithName(name)
	return f
}

func (f *hostFunctionBuilder) WithParameterNames(names ...string) wazero.HostFunctionBuilder {
	f.function = f.function.WithParameterNames(names...)
	return f
}

func (f *hostFunctionBuilder) WithResultNames(names ...string) wazero.HostFunctionBuilder {
	f.function = f.function.WithResultNames(names...)
	return f
}

func (f *hostFunctionBuilder) Export(name string) wazero.HostModuleBuilder {
	f.function.Export(name)
	return f.module
}

func (b *hostBuilder) Compile(ctx context.Context) (wazero.CompiledModule, error) {
	if b.runtime.BeforeHostCompile != nil {
		if err := b.runtime.BeforeHostCompile(); err != nil {
			return nil, err
		}
	}
	code, err := b.HostModuleBuilder.Compile(ctx)
	if err != nil {
		return nil, err
	}
	return b.runtime.track(ctx, code, true), nil
}

func (b *hostBuilder) Instantiate(context.Context) (api.Module, error) {
	return nil, errors.New("tracking runtime requires Compile followed by InstantiateModule")
}

type Code struct {
	wazero.CompiledModule
	Host   bool
	Closes atomic.Int64
	live   *atomic.Int64
}

func (c *Code) Close(ctx context.Context) error {
	err := c.CompiledModule.Close(ctx)
	if err == nil && c.Closes.Add(1) == 1 {
		c.live.Add(-1)
	}
	return err
}
