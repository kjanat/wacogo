package types

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
)

// forbiddenHeaders are request/response header names a guest is not allowed
// to set or override — they would let the guest impersonate transport-level
// state (Host header), violate framing (Content-Length), or hijack
// connection semantics (Connection, Upgrade, Trailer, Transfer-Encoding).
// Comparison is case-insensitive.
var forbiddenHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"connection":        {},
	"transfer-encoding": {},
	"upgrade":           {},
	"trailer":           {},
	"te":                {},
	"keep-alive":        {},
	"proxy-connection":  {},
}

func isForbiddenHeader(name string) bool {
	_, ok := forbiddenHeaders[strings.ToLower(name)]
	return ok
}

type fieldsImpl struct {
	entries   []TupleStringListU8
	immutable bool
}

func newFieldsImpl(entries []TupleStringListU8, immutable bool) *fieldsImpl {
	return &fieldsImpl{entries: cloneEntries(entries), immutable: immutable}
}

func fieldsFromHeader(h http.Header, immutable bool) *fieldsImpl {
	var entries []TupleStringListU8
	for _, name := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[name] {
			entries = append(entries, TupleStringListU8{F0: name, F1: []uint8(v)})
		}
	}
	return &fieldsImpl{entries: entries, immutable: immutable}
}

func cloneEntries(entries []TupleStringListU8) []TupleStringListU8 {
	out := make([]TupleStringListU8, len(entries))
	for i, e := range entries {
		out[i] = TupleStringListU8{F0: e.F0, F1: slices.Clone(e.F1)}
	}
	return out
}

func sameFieldName(a, b string) bool {
	return http.CanonicalHeaderKey(a) == http.CanonicalHeaderKey(b)
}

func (hf *fieldsImpl) remove(name string) {
	hf.entries = slices.DeleteFunc(hf.entries, func(e TupleStringListU8) bool { return sameFieldName(e.F0, name) })
}

func (hf *fieldsImpl) Append(ctx context.Context, name string, value []uint8) (Result_HeaderError, error) {
	if hf.immutable {
		return Result_HeaderErrorErr{
			Value: types.HeaderErrorImmutable{},
		}, nil
	}
	if isForbiddenHeader(name) {
		return Result_HeaderErrorErr{Value: types.HeaderErrorForbidden{}}, nil
	}
	hf.entries = append(hf.entries, TupleStringListU8{F0: name, F1: slices.Clone(value)})
	return types.Result_HeaderErrorOk{}, nil
}

func (hf *fieldsImpl) Clone(ctx context.Context) (*FieldsHandle, error) {
	return types.NewFieldsHandle(newFieldsImpl(hf.entries, false)), nil
}

func (hf *fieldsImpl) Delete(ctx context.Context, name string) (Result_HeaderError, error) {
	if hf.immutable {
		return Result_HeaderErrorErr{
			Value: types.HeaderErrorImmutable{},
		}, nil
	}
	hf.remove(name)
	return types.Result_HeaderErrorOk{}, nil
}

func (hf *fieldsImpl) Entries(ctx context.Context) ([]TupleStringListU8, error) {
	return cloneEntries(hf.entries), nil
}

func (hf *fieldsImpl) Get(ctx context.Context, name string) ([][]uint8, error) {
	result := [][]uint8{}
	for _, e := range hf.entries {
		if sameFieldName(e.F0, name) {
			result = append(result, slices.Clone(e.F1))
		}
	}
	return result, nil
}

func (hf *fieldsImpl) Has(ctx context.Context, name string) (bool, error) {
	return slices.ContainsFunc(hf.entries, func(e TupleStringListU8) bool { return sameFieldName(e.F0, name) }), nil
}

func (hf *fieldsImpl) Set(ctx context.Context, name string, value [][]uint8) (Result_HeaderError, error) {
	if hf.immutable {
		return Result_HeaderErrorErr{
			Value: types.HeaderErrorImmutable{},
		}, nil
	}
	if isForbiddenHeader(name) {
		return Result_HeaderErrorErr{Value: types.HeaderErrorForbidden{}}, nil
	}
	hf.remove(name)
	for _, v := range value {
		hf.entries = append(hf.entries, TupleStringListU8{F0: name, F1: slices.Clone(v)})
	}
	return types.Result_HeaderErrorOk{}, nil
}

func entriesFromFields(ctx context.Context, fh *FieldsHandle) ([]TupleStringListU8, error) {
	if impl, ok := fh.LocalImpl(); ok {
		if fieldsImpl, ok := impl.(*fieldsImpl); ok {
			return cloneEntries(fieldsImpl.entries), nil
		}
	}
	return fh.Entries(ctx)
}

func headerFromEntries(entries []TupleStringListU8) http.Header {
	h := make(http.Header)
	for _, e := range entries {
		h.Add(e.F0, string(e.F1))
	}
	return h
}
