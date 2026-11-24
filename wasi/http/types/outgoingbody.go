package types

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

type outgoingBodyImpl struct {
	request *outgoingRequestImpl
	length  contentLength
	written atomic.Uint64

	pipe          *bodyPipe
	writeOpened   bool
	streamDropped bool
	selfDropped   bool
}

func newOutgoingBodyImpl(request *outgoingRequestImpl) *outgoingBodyImpl {
	return &outgoingBodyImpl{
		request: request,
		length:  request.length,
		pipe:    newBodyPipe(),
	}
}

func bodySizeError(written, declared uint64) *CodedError {
	return &CodedError{
		Code: ErrorCodeHTTPRequestBodySize{Value: SomeU64(written)},
		Msg:  fmt.Sprintf("wasi:http/types.outgoing-body: %d bytes written, content-length is %d", written, declared),
	}
}

func (b *outgoingBodyImpl) sizeMismatch() *CodedError {
	if !b.length.declared {
		return nil
	}
	if written := b.written.Load(); written != b.length.value {
		return bodySizeError(written, b.length.value)
	}
	return nil
}

func (b *outgoingBodyImpl) Write(_ context.Context) (ResultOutputStream_, error) {
	if b.writeOpened {
		return types.ResultOutputStream_Err{}, nil
	}
	b.writeOpened = true
	stream := streams.NewIOWriterOutputStream(b.request.errorInst, b.request.pollInst, &outgoingBodyWriter{Writer: b.pipe, body: b})
	handle := streams.NewOutputStreamHandleIn(b.request.streamInst, stream)
	return types.ResultOutputStream_Ok{Value: handle}, nil
}

// Drop releases the body. Per spec, if an output stream was opened
// from this body it must be dropped first; calling Drop while the
// stream is still live returns an error and does not tear the body
// down. Use DestroyOrphan for orphan-time cleanup that bypasses this
// check.
func (b *outgoingBodyImpl) Drop(_ context.Context) error {
	if b.writeOpened && !b.streamDropped {
		return fmt.Errorf("wasi:http/types.outgoing-body: body dropped without finishing")
	}
	b.destroy()
	return nil
}

// DestroyOrphan tears the body down regardless of the stream's drop
// status. Invoked from instance close drain when the guest never
// followed the drop protocol.
func (b *outgoingBodyImpl) DestroyOrphan(_ context.Context) error {
	b.destroy()
	return nil
}

func (b *outgoingBodyImpl) destroy() {
	if b.selfDropped {
		return
	}
	b.selfDropped = true
	b.pipe.closeWithError(io.ErrUnexpectedEOF)
}

type outgoingBodyWriter struct {
	io.Writer
	body *outgoingBodyImpl
}

func (b *outgoingBodyWriter) Write(p []byte) (int, error) {
	if b.body.length.declared {
		if total := b.body.written.Load() + uint64(len(p)); total > b.body.length.value {
			b.body.written.Store(total)
			return 0, bodySizeError(total, b.body.length.value)
		}
	}
	n, err := b.Writer.Write(p)
	b.body.written.Add(uint64(n))
	return n, err
}

func (b *outgoingBodyWriter) Close() error {
	b.body.streamDropped = true
	return nil
}
