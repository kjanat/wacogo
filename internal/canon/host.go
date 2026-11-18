package canon

import (
	"context"
	"errors"
	"sync"

	"github.com/kjanat/wazero"
)

// Host owns the shared code for the canonical adapters of one Engine.
type Host struct {
	runtime wazero.Runtime

	mu          sync.Mutex
	batchHelper wazero.CompiledModule
	closed      bool
}

func NewHost(runtime wazero.Runtime) *Host { return &Host{runtime: runtime} }

// compiledBatchHelper reuses one helper regardless of the guest export name.
// Compile only on demand and retry if a caller fails to compile it.
func (h *Host) compiledBatchHelper(ctx context.Context) (wazero.CompiledModule, error) {
	h.mu.Lock()
	current, closed := h.batchHelper, h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("wacogo: canonical host is closed")
	}
	if current != nil {
		return current, nil
	}
	// Compile may invoke an embedder's listener factory. Don't hold the
	// lock across callbacks, which may reenter Engine.Close.
	code, err := h.runtime.CompileModule(ctx, encodeBatchReallocHelperModule())
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	current, closed = h.batchHelper, h.closed
	if !closed && current == nil {
		h.batchHelper = code
		h.mu.Unlock()
		return code, nil
	}
	h.mu.Unlock()
	// A racing caller installed the same helper, or closed the engine.
	err = code.Close(context.WithoutCancel(ctx))
	if closed {
		return nil, errors.Join(errors.New("wacogo: canonical host is closed"), err)
	}
	return current, err
}

// Close releases the shared helper explicitly, including when the backing
// wazero compilation cache outlives this runtime.
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	code := h.batchHelper
	h.batchHelper = nil
	h.mu.Unlock()
	if code == nil {
		return nil
	}
	return code.Close(ctx)
}
