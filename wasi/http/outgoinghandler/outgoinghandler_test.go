package outgoinghandler

import (
	"context"
	"net/http"
	"testing"

	"github.com/kjanat/wacogo/wasi/http/types"
)

type bodilessRequest struct {
	types.OutgoingRequest
	r *http.Request
}

func (b bodilessRequest) ToHTTPRequest() *http.Request { return b.r }

type unusedDoer struct{ t *testing.T }

func (d unusedDoer) Do(*http.Request) (*http.Response, error) {
	d.t.Error("request sent")
	return nil, http.ErrNotSupported
}

func TestHandleRejectsContentLengthWithoutBody(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, Header: http.Header{}, ContentLength: 5}
	res, err := (&impl{httpClient: unusedDoer{t}}).Handle(context.Background(), types.NewOutgoingRequestHandle(bodilessRequest{r: req}), OptionRequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.(ResultFutureIncomingResponseTypesErrorCodeErr)
	if want := (types.ErrorCodeHTTPRequestBodySize{Value: types.SomeU64(0)}); !ok || got.Value != want {
		t.Fatalf("handle = %#v, want err(%#v)", res, want)
	}
}
