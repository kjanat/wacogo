package types

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
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
		for kind, release := range map[string]func(*trackedBody) error{
			"drop": func(b *trackedBody) error {
				return (&incomingBodyImpl{resp: &http.Response{Body: b}}).Drop(context.Background())
			},
			"stream": func(b *trackedBody) error {
				s := &incomingBodyStreamImpl{ReadCloser: b}
				err := s.Close()
				if !s.closed {
					t.Errorf("%s/stream: stream not marked closed", name)
				}
				return err
			},
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				rest := strings.NewReader("rest")
				body := &trackedBody{Reader: rest, readErr: c.readErr, closeErr: c.closeErr}
				err := release(body)
				if !body.closed {
					t.Fatal("body not closed")
				}
				for _, want := range []error{c.readErr, c.closeErr} {
					if want != nil && !errors.Is(err, want) {
						t.Fatalf("err = %v, want %v", err, want)
					}
				}
				if c.readErr == nil && c.closeErr == nil && err != nil {
					t.Fatal(err)
				}
				if c.readErr == nil && rest.Len() != 0 {
					t.Fatal("body not drained")
				}
			})
		}
	}
}

func TestIncomingBodyFinishDropsBody(t *testing.T) {
	ctx := context.Background()
	body := &trackedBody{Reader: strings.NewReader("rest")}
	trailer := http.Header{"X-T": {"v"}}
	fh, err := (&impl{}).IncomingBodyFinish(ctx, NewIncomingBodyHandle(&incomingBodyImpl{resp: &http.Response{Body: body, Trailer: trailer}}))
	if err != nil || fh == nil || !body.closed {
		t.Fatalf("finish = %v, %v; closed=%v", fh, err, body.closed)
	}

	open := &incomingBodyImpl{resp: &http.Response{Body: body}, stream: &incomingBodyStreamImpl{ReadCloser: body}}
	fh, err = (&impl{}).IncomingBodyFinish(ctx, NewIncomingBodyHandle(open))
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
		pr, pw := io.Pipe()
		body := &outgoingBodyImpl{request: req, reader: pr, writer: pw, writeOpened: true, streamDropped: true}
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
		if body.writeOpened || body.writer != nil {
			t.Fatal("writer still open")
		}
		if n, err := pr.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			t.Fatalf("read after finish = %d, %v", n, err)
		}
	})

	t.Run("trailer errors", func(t *testing.T) {
		entriesErr, dropErr := errors.New("entries"), errors.New("drop")
		body := &outgoingBodyImpl{request: newOutgoingRequestImpl(nil, nil, nil, nil), streamDropped: true}
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
