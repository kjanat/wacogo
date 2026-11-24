package types

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
	"github.com/kjanat/wacogo/wasi/io/streams"
)

type receivedRequest struct {
	contentLength    int64
	transferEncoding []string
	body             string
	readErr          error
}

func recordingServer(t *testing.T) (*httptest.Server, <-chan receivedRequest) {
	t.Helper()
	got := make(chan receivedRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		got <- receivedRequest{contentLength: r.ContentLength, transferEncoding: r.TransferEncoding, body: string(body), readErr: err}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

type sentRequest struct {
	finish  Result_ErrorCode
	writes  []streams.StreamError
	doErr   error
	request *outgoingRequestImpl
}

func sendWithContentLength(t *testing.T, srv *httptest.Server, length string, chunks ...string) sentRequest {
	t.Helper()
	ctx := context.Background()
	var headers []TupleStringListU8
	if length != "" {
		headers = append(headers, entry("Content-Length", length))
	}
	rh, err := (&impl{}).NewOutgoingRequest(ctx, NewFieldsHandle(newFieldsImpl(headers, false)))
	if err != nil {
		t.Fatal(err)
	}
	local, _ := rh.LocalImpl()
	req, ok := local.(*outgoingRequestImpl)
	if !ok {
		t.Fatalf("request impl = %T", local)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req.r.URL = u
	req.r.Method = http.MethodPost

	bodyRes, err := req.Body(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, ok := bodyRes.(types.ResultOutgoingBody_Ok)
	if !ok {
		t.Fatalf("body = %#v", bodyRes)
	}
	var sent sentRequest
	sent.request = req
	if chunks != nil {
		streamRes, err := body.Value.Write(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stream, ok := streamRes.(types.ResultOutputStream_Ok)
		if !ok {
			t.Fatalf("write = %#v", streamRes)
		}
		done := make(chan error, 1)
		go func() {
			resp, err := srv.Client().Do(req.ToHTTPRequest())
			if err == nil {
				_ = resp.Body.Close()
			}
			done <- err
		}()
		for _, chunk := range chunks {
			res, err := stream.Value.BlockingWriteAndFlush(ctx, []uint8(chunk))
			if err != nil {
				t.Fatal(err)
			}
			if e, failed := res.(streams.Result_StreamErrorErr); failed {
				sent.writes = append(sent.writes, e.Value)
			}
		}
		if err := stream.Value.Drop(ctx); err != nil {
			t.Fatal(err)
		}
		sent.finish, err = (&impl{}).OutgoingBodyFinish(ctx, body.Value, OptionFields{})
		if err != nil {
			t.Fatal(err)
		}
		sent.doErr = <-done
		return sent
	}
	sent.finish, err = (&impl{}).OutgoingBodyFinish(ctx, body.Value, OptionFields{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req.ToHTTPRequest())
	if err == nil {
		_ = resp.Body.Close()
	}
	sent.doErr = err
	return sent
}

func requireBodySizeError(t *testing.T, res Result_ErrorCode, written uint64) {
	t.Helper()
	e, ok := res.(Result_ErrorCodeErr)
	if !ok {
		t.Fatalf("finish = %#v, want body-size error", res)
	}
	if want := (ErrorCodeHTTPRequestBodySize{Value: SomeU64(written)}); e.Value != want {
		t.Fatalf("finish error = %#v, want %#v", e.Value, want)
	}
}

func TestContentLengthSetsRequestLength(t *testing.T) {
	srv, got := recordingServer(t)
	sent := sendWithContentLength(t, srv, "5", "hel", "lo")
	if _, ok := sent.finish.(types.Result_ErrorCodeOk); !ok || sent.doErr != nil || len(sent.writes) != 0 {
		t.Fatalf("finish = %#v, do = %v, write errors = %v", sent.finish, sent.doErr, sent.writes)
	}
	if sent.request.r.ContentLength != 5 {
		t.Fatalf("request ContentLength = %d, want 5", sent.request.r.ContentLength)
	}
	r := <-got
	if r.contentLength != 5 || len(r.transferEncoding) != 0 || r.body != "hello" || r.readErr != nil {
		t.Fatalf("server received %+v", r)
	}
}

func TestContentLengthZeroSendsEmptyBody(t *testing.T) {
	srv, got := recordingServer(t)
	sent := sendWithContentLength(t, srv, "0")
	if _, ok := sent.finish.(types.Result_ErrorCodeOk); !ok || sent.doErr != nil {
		t.Fatalf("finish = %#v, do = %v", sent.finish, sent.doErr)
	}
	r := <-got
	if r.contentLength != 0 || len(r.transferEncoding) != 0 || r.body != "" {
		t.Fatalf("server received %+v", r)
	}
}

func TestContentLengthShortBodyFailsFinish(t *testing.T) {
	srv, got := recordingServer(t)
	sent := sendWithContentLength(t, srv, "5", "hi")
	requireBodySizeError(t, sent.finish, 2)
	if sent.doErr == nil {
		t.Fatal("request with a short body succeeded")
	}
	select {
	case r := <-got:
		if r.readErr == nil {
			t.Fatalf("server received a complete body after an aborted finish: %+v", r)
		}
	default:
	}
}

func TestContentLengthWithoutWriteFailsFinish(t *testing.T) {
	srv, _ := recordingServer(t)
	sent := sendWithContentLength(t, srv, "3")
	requireBodySizeError(t, sent.finish, 0)
}

func TestContentLengthOverflowFailsWriteAndFinish(t *testing.T) {
	srv, _ := recordingServer(t)
	sent := sendWithContentLength(t, srv, "2", "hello")
	if len(sent.writes) == 0 {
		t.Fatal("overflowing write succeeded")
	}
	if _, ok := sent.writes[0].(streams.StreamErrorLastOperationFailed); !ok {
		t.Fatalf("write error = %#v, want last-operation-failed", sent.writes[0])
	}
	requireBodySizeError(t, sent.finish, 5)
}

func TestInvalidContentLengthRefusesBody(t *testing.T) {
	ctx := context.Background()
	for name, headers := range map[string][]TupleStringListU8{
		"not digits": {entry("Content-Length", "abc")},
		"two values": {entry("Content-Length", "1"), entry("content-length", "1")},
		"too large":  {entry("Content-Length", "9223372036854775808")},
	} {
		t.Run(name, func(t *testing.T) {
			rh, err := (&impl{}).NewOutgoingRequest(ctx, NewFieldsHandle(foreignFields{newFieldsImpl(headers, false)}))
			if err != nil {
				t.Fatal(err)
			}
			res, err := rh.Body(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, refused := res.(types.ResultOutgoingBody_Err); !refused {
				t.Fatalf("body = %#v, want err", res)
			}
		})
	}
}

func TestContentLengthOverflowErrorCode(t *testing.T) {
	srv, _ := recordingServer(t)
	sent := sendWithContentLength(t, srv, "2", "hello")
	if len(sent.writes) == 0 {
		t.Fatal("overflowing write succeeded")
	}
	failed, ok := sent.writes[0].(streams.StreamErrorLastOperationFailed)
	if !ok {
		t.Fatalf("write error = %#v, want last-operation-failed", sent.writes[0])
	}
	got, err := (&impl{}).HTTPErrorCode(context.Background(), failed.Value)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ErrorCodeHTTPRequestBodySize{Value: SomeU64(5)}); !got.IsSome || got.Value != want {
		t.Fatalf("http-error-code = %#v, want some(%#v)", got, want)
	}
}

func TestContentLengthShortBodyFailsRequest(t *testing.T) {
	srv, _ := recordingServer(t)
	sent := sendWithContentLength(t, srv, "5", "hi")
	if got := ErrorCodeOf(sent.doErr); got != (ErrorCodeHTTPProtocolError{}) {
		t.Fatalf("request error %v = %#v, want HTTP-protocol-error", sent.doErr, got)
	}
}

func TestHTTPErrorCodeOfOtherStreamFailure(t *testing.T) {
	ctx := context.Background()
	s := streams.NewIOWriterOutputStream(nil, nil, failingWriter{})
	defer s.Drop(ctx)
	res, err := s.BlockingWriteAndFlush(ctx, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := res.(streams.Result_StreamErrorErr)
	if !ok {
		t.Fatalf("blocking-write-and-flush = %#v, want err", res)
	}
	failed, ok := e.Value.(streams.StreamErrorLastOperationFailed)
	if !ok {
		t.Fatalf("stream error = %#v, want last-operation-failed", e.Value)
	}
	got, err := (&impl{}).HTTPErrorCode(ctx, failed.Value)
	if err != nil || got.IsSome {
		t.Fatalf("http-error-code = %#v, %v, want none", got, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrShortWrite }

func TestBodyWrittenAroundSend(t *testing.T) {
	for name, sendFirst := range map[string]bool{"send then write": true, "write then send": false} {
		t.Run(name, func(t *testing.T) {
			srv, got := recordingServer(t)
			ctx := context.Background()
			rh, err := (&impl{}).NewOutgoingRequest(ctx, NewFieldsHandle(newFieldsImpl(nil, false)))
			if err != nil {
				t.Fatal(err)
			}
			local, _ := rh.LocalImpl()
			req, ok := local.(*outgoingRequestImpl)
			if !ok {
				t.Fatalf("request impl = %T", local)
			}
			u, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			req.r.URL = u
			req.r.Method = http.MethodPost
			bodyRes, err := req.Body(ctx)
			if err != nil {
				t.Fatal(err)
			}
			body, ok := bodyRes.(types.ResultOutgoingBody_Ok)
			if !ok {
				t.Fatalf("body = %#v", bodyRes)
			}
			done := make(chan error, 1)
			send := func() {
				go func() {
					resp, err := srv.Client().Do(req.ToHTTPRequest())
					if err == nil {
						_ = resp.Body.Close()
					}
					done <- err
				}()
			}
			if sendFirst {
				send()
			}
			streamRes, err := body.Value.Write(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stream, ok := streamRes.(types.ResultOutputStream_Ok)
			if !ok {
				t.Fatalf("write = %#v", streamRes)
			}
			if res, err := stream.Value.BlockingWriteAndFlush(ctx, []uint8("hello")); err != nil {
				t.Fatal(err)
			} else if _, ok := res.(streams.Result_StreamErrorOk); !ok {
				t.Fatalf("blocking-write-and-flush = %#v", res)
			}
			if !sendFirst {
				send()
			}
			if err := stream.Value.Drop(ctx); err != nil {
				t.Fatal(err)
			}
			if res, err := (&impl{}).OutgoingBodyFinish(ctx, body.Value, OptionFields{}); err != nil {
				t.Fatal(err)
			} else if _, ok := res.(types.Result_ErrorCodeOk); !ok {
				t.Fatalf("finish = %#v", res)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if r := <-got; r.body != "hello" || r.readErr != nil {
				t.Fatalf("server received %+v", r)
			}
		})
	}
}
