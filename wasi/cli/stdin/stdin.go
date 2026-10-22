// Package stdin is the wasi:cli/stdin host component. It exposes a
// single input stream backed by an io.Reader supplied by the embedder.
package stdin

import (
	"bytes"
	"context"
	"io"

	"github.com/kjanat/wacogo"
	"github.com/kjanat/wacogo/host"
	gen "github.com/kjanat/wacogo/internal/wasi/gen/wasi/cli/stdin"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

// InterfaceName is the canonical WIT id of this interface.
const InterfaceName = gen.InterfaceName

type Stdin = gen.Stdin

type impl struct {
	stdin *streams.InputStreamHandle
}

func (i *impl) GetStdin(_ context.Context) (*streams.InputStreamHandle, error) {
	return i.stdin, nil
}

var _ gen.Stdin = (*impl)(nil)

func NewInstance(ctx context.Context, engine *wacogo.Engine, streamsInst, errorInst, pollInst *host.ComponentInstance, stdin io.Reader, opts ...host.InstantiateOption) (*host.ComponentInstance, error) {
	fac, err := gen.NewFactory(ctx, engine)
	if err != nil {
		return nil, err
	}
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	is := streams.NewIOReaderInputStream(errorInst, pollInst, stdin)
	handle := streams.NewInputStreamHandleIn(streamsInst, is)
	return fac.NewInstance(ctx, &impl{stdin: handle}, &gen.Deps{
		Error:   errorInst.Core(),
		Poll:    pollInst.Core(),
		Streams: streamsInst.Core(),
	}, opts...)
}
