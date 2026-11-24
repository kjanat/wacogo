package types

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/kjanat/wacogo/host"
	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
	"github.com/kjanat/wacogo/wasi/io/poll"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

type incomingResponseImpl struct {
	resp         *http.Response
	bodyConsumed bool
	errorInst    *host.ComponentInstance
	pollInst     *host.ComponentInstance
	streamsInst  *host.ComponentInstance
}

func NewIncomingResponse(resp *http.Response, errorInst, pollInst, streamsInst *host.ComponentInstance) *incomingResponseImpl {
	return &incomingResponseImpl{
		resp:        resp,
		errorInst:   errorInst,
		pollInst:    pollInst,
		streamsInst: streamsInst,
	}
}

func (r *incomingResponseImpl) Consume(_ context.Context) (ResultIncomingBody_, error) {
	if r.bodyConsumed {
		return types.ResultIncomingBody_Err{}, nil
	}
	r.bodyConsumed = true
	return types.ResultIncomingBody_Ok{
		Value: NewIncomingBodyHandle(newIncomingBody(r.resp, r.errorInst, r.pollInst, r.streamsInst)),
	}, nil
}

func newIncomingBody(resp *http.Response, errorInst, pollInst, streamsInst *host.ComponentInstance) *incomingBodyImpl {
	return &incomingBodyImpl{
		resp:        resp,
		received:    newBodyReceiver(resp.Body),
		errorInst:   errorInst,
		pollInst:    pollInst,
		streamsInst: streamsInst,
	}
}

func (r *incomingResponseImpl) Headers(_ context.Context) (*FieldsHandle, error) {
	return NewFieldsHandle(fieldsFromHeader(r.resp.Header, true)), nil
}

func (r *incomingResponseImpl) Status(_ context.Context) (uint16, error) {
	return uint16(r.resp.StatusCode), nil
}

type incomingBodyImpl struct {
	resp        *http.Response
	received    *bodyReceiver
	stream      *incomingBodyStreamImpl
	finished    bool
	errorInst   *host.ComponentInstance
	pollInst    *host.ComponentInstance
	streamsInst *host.ComponentInstance
}

const bodyChunkSize = 32 << 10

type bodyReceiver struct {
	body   io.ReadCloser
	start  sync.Once
	chunks chan []byte
	err    error

	closeOnce sync.Once
	closed    chan struct{}
}

func newBodyReceiver(body io.ReadCloser) *bodyReceiver {
	return &bodyReceiver{body: body, chunks: make(chan []byte), closed: make(chan struct{})}
}

func (r *bodyReceiver) receive() <-chan []byte {
	r.start.Do(func() { go r.fetch() })
	return r.chunks
}

func (r *bodyReceiver) fetch() {
	defer close(r.chunks)
	for {
		buf := make([]byte, bodyChunkSize)
		n, err := r.body.Read(buf)
		if n > 0 {
			select {
			case r.chunks <- buf[:n]:
			case <-r.closed:
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				r.err = err
			}
			return
		}
	}
}

func (r *bodyReceiver) end() error {
	if r.err != nil {
		return receiveError{r.err}
	}
	return io.EOF
}

func (r *bodyReceiver) close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return r.body.Close()
}

// Drop matches the Drop(ctx) error shape that the witgen-emitted
// resourceDtor type-asserts on guest-driven resource.drop, and that
// the host-extern-table drain calls at close. The spec requires the
// body's input-stream child to be dropped first; violating that
// returns an error.
func (b *incomingBodyImpl) Drop(_ context.Context) error {
	if b.finished {
		return nil
	}
	if b.stream != nil && !b.stream.closed {
		return fmt.Errorf("wasi:http/types.incoming-body.drop: body stream not closed")
	}
	return b.received.close()
}

func (b *incomingBodyImpl) finish(pollInst *host.ComponentInstance) (*futureTrailersImpl, error) {
	if b.stream != nil && !b.stream.closed {
		return nil, fmt.Errorf("body stream not closed")
	}
	b.finished = true
	f := &futureTrailersImpl{done: make(chan struct{}), pollInst: pollInst}
	go func() {
		defer close(f.done)
		for range b.received.receive() {
		}
		_ = b.received.close()
		f.err = b.received.err
		if f.err == nil {
			f.trailers = b.resp.Trailer
		}
	}()
	return f, nil
}

func (b *incomingBodyImpl) Stream(_ context.Context) (ResultInputStream_, error) {
	if b.stream != nil {
		return types.ResultInputStream_Err{}, nil
	}
	b.stream = &incomingBodyStreamImpl{received: b.received, stopped: make(chan struct{})}
	h := streams.NewInputStreamHandleIn(b.streamsInst, &bodyInputStream{
		IOReaderInputStream: streams.NewIOReaderInputStream(b.errorInst, b.pollInst, b.stream),
		body:                b.stream,
	})
	return types.ResultInputStream_Ok{Value: h}, nil
}

type incomingBodyStreamImpl struct {
	received *bodyReceiver
	pending  []byte
	closed   bool

	stopOnce sync.Once
	stopped  chan struct{}
}

func (s *incomingBodyStreamImpl) Read(p []byte) (int, error) {
	if len(s.pending) == 0 {
		select {
		case chunk, ok := <-s.received.receive():
			if !ok {
				return 0, s.received.end()
			}
			s.pending = chunk
		case <-s.stopped:
			return 0, io.ErrClosedPipe
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *incomingBodyStreamImpl) Close() error {
	s.closed = true
	return nil
}

func (s *incomingBodyStreamImpl) stop() {
	s.stopOnce.Do(func() { close(s.stopped) })
}

type bodyInputStream struct {
	*streams.IOReaderInputStream
	body *incomingBodyStreamImpl
}

func (s *bodyInputStream) Drop(ctx context.Context) error {
	s.body.stop()
	return s.IOReaderInputStream.Drop(ctx)
}

func (s *bodyInputStream) DestroyOrphan(ctx context.Context) error {
	s.body.stop()
	return s.IOReaderInputStream.DestroyOrphan(ctx)
}

type futureTrailersImpl struct {
	done      chan struct{}
	err       error
	trailers  http.Header
	delivered bool
	pollInst  *host.ComponentInstance
}

func (f *futureTrailersImpl) Get(_ context.Context) (OptionResultResultOptionFieldsErrorCode_, error) {
	select {
	case <-f.done:
	default:
		return OptionResultResultOptionFieldsErrorCode_{}, nil
	}
	if f.delivered {
		return OptionResultResultOptionFieldsErrorCode_{IsSome: true, Value: types.ResultResultOptionFieldsErrorCode_Err{}}, nil
	}
	f.delivered = true
	var res ResultOptionFieldsErrorCode = types.ResultOptionFieldsErrorCodeOk{}
	if f.err != nil {
		res = types.ResultOptionFieldsErrorCodeErr{Value: receiveErrorCode(f.err)}
	} else if fields := fieldsFromHeader(f.trailers, true); len(fields.entries) > 0 {
		res = types.ResultOptionFieldsErrorCodeOk{Value: OptionFields{IsSome: true, Value: NewFieldsHandle(fields)}}
	}
	return OptionResultResultOptionFieldsErrorCode_{IsSome: true, Value: types.ResultResultOptionFieldsErrorCode_Ok{Value: res}}, nil
}

func (f *futureTrailersImpl) Subscribe(_ context.Context) (*poll.PollableHandle, error) {
	return poll.NewPollableHandleIn(f.pollInst, futureTrailersPollable{done: f.done}), nil
}

type futureTrailersPollable struct {
	done chan struct{}
}

func (p futureTrailersPollable) Block(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p futureTrailersPollable) Ready(_ context.Context) (bool, error) {
	select {
	case <-p.done:
		return true, nil
	default:
		return false, nil
	}
}

func (p futureTrailersPollable) Done() <-chan struct{} {
	return p.done
}
