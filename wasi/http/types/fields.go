package types

import (
	"context"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
)

// forbiddenHeaders matches DEFAULT_FORBIDDEN_HEADERS in wasmtime-wasi-http.
var forbiddenHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"host":                {},
	"http2-settings":      {},
}

func isForbiddenHeader(name string) bool {
	_, ok := forbiddenHeaders[strings.ToLower(name)]
	return ok
}

func validFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func validFieldValue(value []byte) bool {
	for _, b := range value {
		if b != '\t' && (b < 0x20 || b == 0x7f) {
			return false
		}
	}
	return true
}

func isContentLength(name string) bool {
	return strings.EqualFold(name, "content-length")
}

func parseFieldValue(name string, value []byte) ([]byte, bool) {
	if !isContentLength(name) {
		return slices.Clone(value), validFieldValue(value)
	}
	n, ok := parseContentLength(value)
	if !ok {
		return nil, false
	}
	return []byte(strconv.FormatUint(n, 10)), true
}

func parseContentLength(value []byte) (uint64, bool) {
	if len(value) == 0 {
		return 0, false
	}
	for _, b := range value {
		if b < '0' || b > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(string(value), 10, 64)
	return n, err == nil
}

type contentLength struct {
	declared bool
	value    uint64
}

func declaredContentLength(entries []TupleStringListU8) (contentLength, bool) {
	var length contentLength
	for _, e := range entries {
		if !isContentLength(e.F0) {
			continue
		}
		n, ok := parseContentLength(e.F1)
		if length.declared || !ok || n > math.MaxInt64 {
			return contentLength{}, false
		}
		length = contentLength{declared: true, value: n}
	}
	return length, true
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
		if isForbiddenHeader(name) {
			continue
		}
		for _, v := range h[name] {
			entries = append(entries, TupleStringListU8{F0: name, F1: []uint8(v)})
		}
	}
	return &fieldsImpl{entries: entries, immutable: immutable}
}

func fieldsFromList(list []TupleStringListU8) (*fieldsImpl, types.HeaderError) {
	hf := &fieldsImpl{}
	for _, e := range list {
		if err := hf.append(e.F0, e.F1); err != nil {
			return nil, err
		}
	}
	return hf, nil
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

func (hf *fieldsImpl) has(name string) bool {
	return validFieldName(name) && slices.ContainsFunc(hf.entries, func(e TupleStringListU8) bool { return sameFieldName(e.F0, name) })
}

func (hf *fieldsImpl) remove(name string) {
	hf.entries = slices.DeleteFunc(hf.entries, func(e TupleStringListU8) bool { return sameFieldName(e.F0, name) })
}

func (hf *fieldsImpl) append(name string, value []byte) types.HeaderError {
	if !validFieldName(name) {
		return types.HeaderErrorInvalidSyntax{}
	}
	if isForbiddenHeader(name) {
		return types.HeaderErrorForbidden{}
	}
	parsed, ok := parseFieldValue(name, value)
	if !ok || isContentLength(name) && hf.has(name) {
		return types.HeaderErrorInvalidSyntax{}
	}
	if hf.immutable {
		return types.HeaderErrorImmutable{}
	}
	hf.entries = append(hf.entries, TupleStringListU8{F0: name, F1: parsed})
	return nil
}

func (hf *fieldsImpl) set(name string, values [][]byte) types.HeaderError {
	if !validFieldName(name) {
		return types.HeaderErrorInvalidSyntax{}
	}
	if isForbiddenHeader(name) {
		return types.HeaderErrorForbidden{}
	}
	if hf.immutable {
		return types.HeaderErrorImmutable{}
	}
	if isContentLength(name) && len(values) > 1 {
		return types.HeaderErrorInvalidSyntax{}
	}
	parsed := make([][]byte, len(values))
	for i, v := range values {
		p, ok := parseFieldValue(name, v)
		if !ok {
			return types.HeaderErrorInvalidSyntax{}
		}
		parsed[i] = p
	}
	hf.remove(name)
	for _, v := range parsed {
		hf.entries = append(hf.entries, TupleStringListU8{F0: name, F1: v})
	}
	return nil
}

func (hf *fieldsImpl) delete(name string) types.HeaderError {
	if !validFieldName(name) {
		return types.HeaderErrorInvalidSyntax{}
	}
	if isForbiddenHeader(name) {
		return types.HeaderErrorForbidden{}
	}
	if hf.immutable {
		return types.HeaderErrorImmutable{}
	}
	hf.remove(name)
	return nil
}

func headerResult(err types.HeaderError) Result_HeaderError {
	if err != nil {
		return Result_HeaderErrorErr{Value: err}
	}
	return types.Result_HeaderErrorOk{}
}

func (hf *fieldsImpl) Append(ctx context.Context, name string, value []uint8) (Result_HeaderError, error) {
	return headerResult(hf.append(name, value)), nil
}

func (hf *fieldsImpl) Clone(ctx context.Context) (*FieldsHandle, error) {
	return types.NewFieldsHandle(newFieldsImpl(hf.entries, false)), nil
}

func (hf *fieldsImpl) Delete(ctx context.Context, name string) (Result_HeaderError, error) {
	return headerResult(hf.delete(name)), nil
}

func (hf *fieldsImpl) Entries(ctx context.Context) ([]TupleStringListU8, error) {
	return cloneEntries(hf.entries), nil
}

func (hf *fieldsImpl) Get(ctx context.Context, name string) ([][]uint8, error) {
	result := [][]uint8{}
	if !validFieldName(name) {
		return result, nil
	}
	for _, e := range hf.entries {
		if sameFieldName(e.F0, name) {
			result = append(result, slices.Clone(e.F1))
		}
	}
	return result, nil
}

func (hf *fieldsImpl) Has(ctx context.Context, name string) (bool, error) {
	return hf.has(name), nil
}

func (hf *fieldsImpl) Set(ctx context.Context, name string, value [][]uint8) (Result_HeaderError, error) {
	return headerResult(hf.set(name, value)), nil
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
