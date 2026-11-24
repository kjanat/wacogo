package types

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestErrorCodeOf_CodedErrorHTTPRequestDenied(t *testing.T) {
	in := &CodedError{Code: ErrorCodeHTTPRequestDenied{}}
	got := ErrorCodeOf(in)
	if _, ok := got.(ErrorCodeHTTPRequestDenied); !ok {
		t.Fatalf("got %T, want ErrorCodeHTTPRequestDenied", got)
	}
}

func TestErrorCodeOf_CodedErrorDestinationIPProhibited(t *testing.T) {
	in := &CodedError{Code: ErrorCodeDestinationIPProhibited{}}
	got := ErrorCodeOf(in)
	if _, ok := got.(ErrorCodeDestinationIPProhibited); !ok {
		t.Fatalf("got %T, want ErrorCodeDestinationIPProhibited", got)
	}
}

func TestErrorCodeOf_CodedErrorWrapped(t *testing.T) {
	inner := &CodedError{Code: ErrorCodeHTTPRequestDenied{}}
	wrapped := fmt.Errorf("doer rejected: %w", inner)
	got := ErrorCodeOf(wrapped)
	if _, ok := got.(ErrorCodeHTTPRequestDenied); !ok {
		t.Fatalf("got %T, want ErrorCodeHTTPRequestDenied (errors.As must unwrap)", got)
	}
}

func TestErrorCodeOf_CodedErrorNilCodeFallsThrough(t *testing.T) {
	in := &CodedError{Code: nil, Msg: "unspecific deny"}
	got := ErrorCodeOf(in)
	internal, ok := got.(ErrorCodeInternalError)
	if !ok {
		t.Fatalf("got %T, want ErrorCodeInternalError fallback", got)
	}
	if !internal.Value.IsSome || internal.Value.Value != "unspecific deny" {
		t.Fatalf("ErrorCodeInternalError.Value = %+v, want SomeString(\"unspecific deny\")", internal.Value)
	}
}

func TestErrorCodeOf_PlainErrorStillFallsBack(t *testing.T) {
	got := ErrorCodeOf(errors.New("blocked"))
	internal, ok := got.(ErrorCodeInternalError)
	if !ok {
		t.Fatalf("got %T, want ErrorCodeInternalError", got)
	}
	if !internal.Value.IsSome || internal.Value.Value != "blocked" {
		t.Fatalf("ErrorCodeInternalError.Value = %+v, want SomeString(\"blocked\")", internal.Value)
	}
}

func TestErrorCodeOfIncompleteMessage(t *testing.T) {
	for _, err := range []error{io.ErrUnexpectedEOF, fmt.Errorf("read body: %w", io.ErrUnexpectedEOF)} {
		if got := ErrorCodeOf(err); got != (ErrorCodeHTTPProtocolError{}) {
			t.Fatalf("ErrorCodeOf(%v) = %#v, want HTTP-protocol-error", err, got)
		}
	}
}
