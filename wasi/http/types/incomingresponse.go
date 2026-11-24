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
		received:    &bodyReceiver{body: resp.Body},
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

type bodyReceiver struct {
	body io.ReadCloser
	mu   sync.Mutex
	err  error
}

func (r *bodyReceiver) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if err == nil || err == io.EOF {
		return n, err
	}
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	return n, receiveError{err}
}

func (r *bodyReceiver) Close() error {
	return r.body.Close()
}

func (r *bodyReceiver) receiveErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Drop matches the Drop(ctx) error shape that the witgen-emitted
// resourceDtor type-asserts on guest-driven resource.drop, and that
// the host-extern-table drain calls at close. The spec requires the
// body's input-stream child to be dropped first; violating that
// returns an error. When no stream was opened, drain the response
// body and close it so the underlying connection can be reused.
func (b *incomingBodyImpl) Drop(_ context.Context) error {
	if b.finished {
		return nil
	}
	if b.stream != nil && !b.stream.closed {
		return fmt.Errorf("wasi:http/types.incoming-body.drop: body stream not closed")
	}
	if b.stream == nil {
		return drainAndClose(b.received)
	}
	return nil
}

func (b *incomingBodyImpl) finish(pollInst *host.ComponentInstance) (*futureTrailersImpl, error) {
	if b.stream != nil && !b.stream.closed {
		return nil, fmt.Errorf("body stream not closed")
	}
	b.finished = true
	f := &futureTrailersImpl{done: make(chan struct{}), pollInst: pollInst}
	drain := b.stream == nil
	go func() {
		defer close(f.done)
		if drain {
			_ = drainAndClose(b.received)
		}
		f.err = b.received.receiveErr()
		if f.err == nil {
			f.trailers = b.resp.Trailer
		}
	}()
	return f, nil
}

func drainAndClose(body io.ReadCloser) error {
	_, _ = io.Copy(io.Discard, body)
	return body.Close()
}

func (b *incomingBodyImpl) Stream(_ context.Context) (ResultInputStream_, error) {
	if b.stream != nil {
		return types.ResultInputStream_Err{}, nil
	}
	b.stream = &incomingBodyStreamImpl{ReadCloser: b.received}
	h := streams.NewInputStreamHandleIn(b.streamsInst, streams.NewIOReaderInputStream(b.errorInst, b.pollInst, b.stream))
	return types.ResultInputStream_Ok{Value: h}, nil
}

type incomingBodyStreamImpl struct {
	io.ReadCloser
	closed bool
}

func (s *incomingBodyStreamImpl) Close() error {
	s.closed = true
	return drainAndClose(s.ReadCloser)
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
