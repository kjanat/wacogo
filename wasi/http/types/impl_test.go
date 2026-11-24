package types

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/io/wioerror"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

type faultyFields struct {
	*fieldsImpl
	entriesErr, dropErr error
}

func (f faultyFields) Entries(ctx context.Context) ([]TupleStringListU8, error) {
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
	return f.fieldsImpl.Entries(ctx)
}

func (f faultyFields) Drop(context.Context) error { return f.dropErr }

func TestNewOutgoingRequestReportsFieldsErrors(t *testing.T) {
	entriesErr, dropErr := errors.New("entries"), errors.New("drop")
	for name, c := range map[string]struct {
		fields faultyFields
		want   []error
	}{
		"entries": {faultyFields{fieldsImpl: newFieldsImpl(nil, false), entriesErr: entriesErr}, []error{entriesErr}},
		"drop":    {faultyFields{fieldsImpl: newFieldsImpl(nil, false), dropErr: dropErr}, []error{dropErr}},
		"both":    {faultyFields{fieldsImpl: newFieldsImpl(nil, false), entriesErr: entriesErr, dropErr: dropErr}, []error{entriesErr, dropErr}},
	} {
		t.Run(name, func(t *testing.T) {
			rh, err := (&impl{}).NewOutgoingRequest(context.Background(), NewFieldsHandle(c.fields))
			if rh != nil {
				t.Fatal("request built from failed fields")
			}
			for _, want := range c.want {
				if !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v", err, want)
				}
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	readErr, closeErr error
	closed            bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.Reader.Read(p)
}

func (b *trackedBody) Close() error {
	b.closed = true
	return b.closeErr
}

func TestIncomingBodyDropDrainsAndCloses(t *testing.T) {
	readErr, closeErr := errors.New("read"), errors.New("close")
	for name, c := range map[string]struct {
		readErr, closeErr error
	}{
		"clean": {nil, nil},
		"read":  {readErr, nil},
		"close": {nil, closeErr},
		"both":  {readErr, closeErr},
	} {
		for kind, k := range map[string]struct {
			release      func(*trackedBody) error
			reportsClose bool
		}{
			"drop": {func(b *trackedBody) error {
				return consumeBody(t, &http.Response{Body: b}).Drop(context.Background())
			}, true},
			"stream": {func(b *trackedBody) error {
				s := &incomingBodyStreamImpl{ReadCloser: b}
				err := s.Close()
				if !s.closed {
					t.Errorf("%s/stream: stream not marked closed", name)
				}
				return err
			}, true},
			"finish": {func(b *trackedBody) error {
				fh, err := (&impl{}).IncomingBodyFinish(context.Background(), consumeBody(t, &http.Response{Body: b}))
				if err == nil {
					waitReady(t, fh)
				}
				return err
			}, false},
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				rest := strings.NewReader("rest")
				body := &trackedBody{Reader: rest, readErr: c.readErr, closeErr: c.closeErr}
				err := k.release(body)
				if !body.closed {
					t.Fatal("body not closed")
				}
				if c.readErr != nil && errors.Is(err, c.readErr) {
					t.Fatalf("drain error became a trap: %v", err)
				}
				if !k.reportsClose && err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if k.reportsClose && c.closeErr != nil && !errors.Is(err, c.closeErr) {
					t.Fatalf("err = %v, want %v", err, c.closeErr)
				}
				if (!k.reportsClose || c.closeErr == nil) && err != nil {
					t.Fatal(err)
				}
				if c.readErr == nil && rest.Len() != 0 {
					t.Fatal("body not drained")
				}
			})
		}
	}
}

func consumeBody(t *testing.T, resp *http.Response) *IncomingBodyHandle {
	t.Helper()
	res, err := NewIncomingResponse(resp, nil, nil, nil).Consume(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(types.ResultIncomingBody_Ok)
	if !isOK {
		t.Fatalf("consume = %#v", res)
	}
	return ok.Value
}

func waitReady(t *testing.T, fh *FutureTrailersHandle) {
	t.Helper()
	ctx := context.Background()
	p, err := fh.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Block(ctx); err != nil {
		t.Fatal(err)
	}
	if ready, err := p.Ready(ctx); err != nil || !ready {
		t.Fatalf("ready = %v, %v after block", ready, err)
	}
}

func TestIncomingBodyFinishDropsBody(t *testing.T) {
	ctx := context.Background()
	body := &trackedBody{Reader: strings.NewReader("rest")}
	fh, err := (&impl{}).IncomingBodyFinish(ctx, consumeBody(t, &http.Response{Body: body}))
	if err != nil || fh == nil {
		t.Fatalf("finish = %v, %v", fh, err)
	}
	waitReady(t, fh)
	if !body.closed {
		t.Fatal("body not closed after finish")
	}

	open := consumeBody(t, &http.Response{Body: body})
	if _, err := open.Stream(ctx); err != nil {
		t.Fatal(err)
	}
	fh, err = (&impl{}).IncomingBodyFinish(ctx, open)
	if fh != nil || err == nil || !strings.Contains(err.Error(), "body stream not closed") {
		t.Fatalf("finish with open stream = %v, %v", fh, err)
	}
}

type foreignBody struct {
	*outgoingBodyImpl
	dropErr error
}

func (b foreignBody) Drop(context.Context) error { return b.dropErr }

func TestOutgoingBodyFinish(t *testing.T) {
	ctx := context.Background()
	trailers := []TupleStringListU8{entry("x-t", "1"), entry("X-T", "2")}

	t.Run("trailers and writer", func(t *testing.T) {
		req := newOutgoingRequestImpl(nil, nil, nil, nil)
		body := newOutgoingBodyImpl(req)
		body.writeOpened, body.streamDropped = true, true
		pr := body.pipe.reader()
		res, err := (&impl{}).OutgoingBodyFinish(ctx, NewOutgoingBodyHandle(body), OptionFields{IsSome: true, Value: NewFieldsHandle(newFieldsImpl(trailers, false))})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := res.(types.Result_ErrorCodeOk); !ok {
			t.Fatalf("result = %#v", res)
		}
		if want := (http.Header{"X-T": {"1", "2"}}); !reflect.DeepEqual(req.r.Trailer, want) {
			t.Fatalf("trailer = %v, want %v", req.r.Trailer, want)
		}
		if n, err := pr.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			t.Fatalf("read after finish = %d, %v", n, err)
		}
	})

	t.Run("trailer errors", func(t *testing.T) {
		entriesErr, dropErr := errors.New("entries"), errors.New("drop")
		body := newOutgoingBodyImpl(newOutgoingRequestImpl(nil, nil, nil, nil))
		fields := faultyFields{fieldsImpl: newFieldsImpl(trailers, false), entriesErr: entriesErr, dropErr: dropErr}
		res, err := (&impl{}).OutgoingBodyFinish(ctx, NewOutgoingBodyHandle(body), OptionFields{IsSome: true, Value: NewFieldsHandle(fields)})
		if res != nil || !errors.Is(err, entriesErr) || !errors.Is(err, dropErr) {
			t.Fatalf("finish = %v, %v", res, err)
		}
	})

	t.Run("body drop error", func(t *testing.T) {
		dropErr := errors.New("drop")
		res, err := (&impl{}).OutgoingBodyFinish(ctx, NewOutgoingBodyHandle(foreignBody{dropErr: dropErr}), OptionFields{})
		if res != nil || !errors.Is(err, dropErr) || !strings.Contains(err.Error(), "invalid outgoing body handle") {
			t.Fatalf("finish = %v, %v", res, err)
		}
	})
}

func resetAfterPartialBody(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		_ = buf.Flush()
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIncomingBodyConnectionReset(t *testing.T) {
	ctx := context.Background()
	srv := resetAfterPartialBody(t)

	t.Run("drop", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err := consumeBody(t, resp).Drop(ctx); err != nil {
			t.Fatalf("drop after reset = %v", err)
		}
	})

	t.Run("stream", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		body := consumeBody(t, resp)
		res, err := body.Stream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ok, isOK := res.(types.ResultInputStream_Ok)
		if !isOK {
			t.Fatalf("stream = %#v", res)
		}
		var got []byte
		var streamErr streams.StreamError
		for streamErr == nil {
			r, err := ok.Value.BlockingRead(ctx, 64)
			if err != nil {
				t.Fatalf("blocking-read trapped: %v", err)
			}
			switch r := r.(type) {
			case streams.ResultListU8StreamErrorOk:
				got = append(got, r.Value...)
			case streams.ResultListU8StreamErrorErr:
				streamErr = r.Value
			}
		}
		if string(got) != "partial" {
			t.Fatalf("read %q, want %q", got, "partial")
		}
		if _, failed := streamErr.(streams.StreamErrorLastOperationFailed); !failed {
			t.Fatalf("stream error = %#v, want last-operation-failed", streamErr)
		}
		if err := ok.Value.Drop(ctx); err != nil {
			t.Fatalf("stream drop after reset = %v", err)
		}
		if _, err := (&impl{}).IncomingBodyFinish(ctx, body); err != nil {
			t.Fatalf("finish after reset = %v", err)
		}
	})
}

func truncatedBody(t *testing.T, reset bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		_ = buf.Flush()
		if tcp, ok := conn.(*net.TCPConn); ok && reset {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func trailersServer(t *testing.T, trailers http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name := range trailers {
			w.Header().Add("Trailer", name)
		}
		_, _ = w.Write([]byte("body"))
		w.(http.Flusher).Flush()
		for name, values := range trailers {
			w.Header()[name] = values
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func incomingBody(t *testing.T, srv *httptest.Server) *IncomingBodyHandle {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return consumeBody(t, resp)
}

func readAndDropStream(t *testing.T, body *IncomingBodyHandle) {
	t.Helper()
	ctx := context.Background()
	res, err := body.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(types.ResultInputStream_Ok)
	if !isOK {
		t.Fatalf("stream = %#v", res)
	}
	for {
		r, err := ok.Value.BlockingRead(ctx, 64)
		if err != nil {
			t.Fatalf("blocking-read trapped: %v", err)
		}
		if _, done := r.(streams.ResultListU8StreamErrorErr); done {
			break
		}
	}
	if err := ok.Value.Drop(ctx); err != nil {
		t.Fatal(err)
	}
}

func finishAndWait(t *testing.T, body *IncomingBodyHandle) *FutureTrailersHandle {
	t.Helper()
	ctx := context.Background()
	fh, err := (&impl{}).IncomingBodyFinish(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, fh)
	return fh
}

func trailersResult(t *testing.T, fh *FutureTrailersHandle) ResultOptionFieldsErrorCode {
	t.Helper()
	ctx := context.Background()
	got, err := fh.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsSome {
		t.Fatal("get = none after the future became ready")
	}
	ok, isOK := got.Value.(types.ResultResultOptionFieldsErrorCode_Ok)
	if !isOK {
		t.Fatalf("first get = %#v", got.Value)
	}
	again, err := fh.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, consumed := again.Value.(types.ResultResultOptionFieldsErrorCode_Err); !again.IsSome || !consumed {
		t.Fatalf("second get = %#v, want some(err)", again)
	}
	return ok.Value
}

func TestFutureTrailersDeliverResponseTrailers(t *testing.T) {
	srv := trailersServer(t, http.Header{"X-Checksum": {"abc"}, "X-Count": {"1", "2"}})
	want := []TupleStringListU8{entry("X-Checksum", "abc"), entry("X-Count", "1"), entry("X-Count", "2")}
	for name, prepare := range map[string]func(*IncomingBodyHandle){
		"finish":      func(*IncomingBodyHandle) {},
		"read stream": func(b *IncomingBodyHandle) { readAndDropStream(t, b) },
	} {
		t.Run(name, func(t *testing.T) {
			body := incomingBody(t, srv)
			prepare(body)
			res, ok := trailersResult(t, finishAndWait(t, body)).(types.ResultOptionFieldsErrorCodeOk)
			if !ok || !res.Value.IsSome {
				t.Fatalf("trailers = %#v, want some", res)
			}
			requireEntries(t, res.Value.Value, want)
			if got := headerErr(t)(res.Value.Value.Append(context.Background(), "x-more", []uint8("v"))); got != immutable {
				t.Fatalf("append to trailers = %#v, want immutable", got)
			}
		})
	}

	t.Run("none", func(t *testing.T) {
		body := incomingBody(t, trailersServer(t, nil))
		res, ok := trailersResult(t, finishAndWait(t, body)).(types.ResultOptionFieldsErrorCodeOk)
		if !ok || res.Value.IsSome {
			t.Fatalf("trailers = %#v, want ok(none)", res)
		}
	})
}

func TestFutureTrailersReportReceiveErrors(t *testing.T) {
	for name, reset := range map[string]bool{"truncated": false, "reset": true} {
		for kind, prepare := range map[string]func(*IncomingBodyHandle){
			"finish":      func(*IncomingBodyHandle) {},
			"read stream": func(b *IncomingBodyHandle) { readAndDropStream(t, b) },
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				body := incomingBody(t, truncatedBody(t, reset))
				prepare(body)
				res, ok := trailersResult(t, finishAndWait(t, body)).(types.ResultOptionFieldsErrorCodeErr)
				if !ok || res.Value != (ErrorCodeHTTPProtocolError{}) {
					t.Fatalf("trailers = %#v, want err(HTTP-protocol-error)", res)
				}
			})
		}
	}
}

func streamFailure(t *testing.T, body *IncomingBodyHandle) *wioerror.ErrorResourceHandle {
	t.Helper()
	ctx := context.Background()
	res, err := body.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := res.(types.ResultInputStream_Ok)
	if !ok {
		t.Fatalf("stream = %#v", res)
	}
	for {
		r, err := stream.Value.BlockingRead(ctx, 64)
		if err != nil {
			t.Fatal(err)
		}
		switch r := r.(type) {
		case streams.ResultListU8StreamErrorErr:
			failed, ok := r.Value.(streams.StreamErrorLastOperationFailed)
			if !ok {
				t.Fatalf("stream ended with %#v, want last-operation-failed", r.Value)
			}
			return failed.Value
		}
	}
}

func TestHTTPErrorCodeOfBodyReadFailure(t *testing.T) {
	for name, reset := range map[string]bool{"truncated": false, "reset": true} {
		t.Run(name, func(t *testing.T) {
			got, err := (&impl{}).HTTPErrorCode(context.Background(), streamFailure(t, incomingBody(t, truncatedBody(t, reset))))
			if err != nil {
				t.Fatal(err)
			}
			if !got.IsSome || got.Value != (ErrorCodeHTTPProtocolError{}) {
				t.Fatalf("http-error-code = %#v, want some(HTTP-protocol-error)", got)
			}
		})
	}
}

func TestFutureTrailersPendingUntilBodyEnds(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx := context.Background()
	fh, err := (&impl{}).IncomingBodyFinish(ctx, incomingBody(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fh.Get(ctx)
	if err != nil || got.IsSome {
		t.Fatalf("get before the body ended = %#v, %v, want none", got, err)
	}
	pollable, err := fh.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := pollable.Ready(ctx); err != nil || ready {
		t.Fatalf("ready before the body ended = %v, %v", ready, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pollable.Block(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("block with canceled context = %v", err)
	}
	close(release)
	waitReady(t, fh)
	if _, ok := trailersResult(t, fh).(types.ResultOptionFieldsErrorCodeOk); !ok {
		t.Fatal("trailers after the body ended are not ok")
	}
}
