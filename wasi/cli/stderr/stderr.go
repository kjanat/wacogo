// Package stderr is the not-yet-implemented wasi:cli/stderr host
// component. Methods return ErrNotImplemented wrapped with their WIT
// call site until a real implementation lands.
package stderr

import (
	"context"
	"io"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	gen "github.com/kjanat/wacogo/internal/wasi/gen/wasi/cli/stderr"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

// InterfaceName is the canonical WIT id of this interface.
const InterfaceName = gen.InterfaceName

type Stderr = gen.Stderr

type impl struct {
	errorInst, pollInst, streamsInst *host.ComponentInstance
	stderr                           io.Writer
}

func (i *impl) GetStderr(_ context.Context) (*streams.OutputStreamHandle, error) {
	os := streams.NewIOWriterOutputStream(i.errorInst, i.pollInst, i.stderr)
	handle := streams.NewOutputStreamHandleIn(i.streamsInst, os)
	return handle, nil
}

var _ gen.Stderr = (*impl)(nil)

func NewInstance(ctx context.Context, engine *wacogo.Engine, streamsInst, errorInst, pollInst *host.ComponentInstance, stderr io.Writer, opts ...host.InstantiateOption) (*host.ComponentInstance, error) {
	fac, err := gen.NewFactory(ctx, engine)
	if err != nil {
		return nil, err
	}
	return fac.NewInstance(ctx, &impl{errorInst: errorInst, pollInst: pollInst, streamsInst: streamsInst, stderr: stderr}, &gen.Deps{
		Error:   errorInst.Core(),
		Poll:    pollInst.Core(),
		Streams: streamsInst.Core(),
	}, opts...)
}
