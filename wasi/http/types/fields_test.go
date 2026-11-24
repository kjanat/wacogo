package types

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/kjanat/wacogo/internal/wasi/gen/wasi/http/types"
)

func entry(name, value string) TupleStringListU8 {
	return TupleStringListU8{F0: name, F1: []uint8(value)}
}

func requireEntries(t *testing.T, fh *FieldsHandle, want []TupleStringListU8) {
	t.Helper()
	got, err := fh.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %q, want %q", got, want)
	}
}

func TestFieldsFromListKeepsOrderCasingAndDuplicates(t *testing.T) {
	list := []TupleStringListU8{
		entry("x-b", "1"),
		entry("Content-Type", "text/plain"),
		entry("X-A", "2"),
		entry("X-B", "3"),
		entry("accept", "a"),
		entry("x-c", "4"),
	}
	for range 32 {
		res, err := (&impl{}).FieldsFromList(context.Background(), list)
		if err != nil {
			t.Fatal(err)
		}
		ok, isOK := res.(types.ResultFieldsHeaderErrorOk)
		if !isOK {
			t.Fatalf("from-list = %#v", res)
		}
		requireEntries(t, ok.Value, list)
	}
}

func TestFieldsMutationsKeepInsertionOrder(t *testing.T) {
	ctx := context.Background()
	fh, err := (&impl{}).NewFields(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []TupleStringListU8{entry("B", "1"), entry("a", "2"), entry("b", "3")} {
		if _, err := fh.Append(ctx, e.F0, e.F1); err != nil {
			t.Fatal(err)
		}
	}
	requireEntries(t, fh, []TupleStringListU8{entry("B", "1"), entry("a", "2"), entry("b", "3")})

	got, err := fh.Get(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]uint8{[]uint8("1"), []uint8("3")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("get = %q, want %q", got, want)
	}
	if has, err := fh.Has(ctx, "A"); err != nil || !has {
		t.Fatalf("has = %v, %v", has, err)
	}

	if _, err := fh.Set(ctx, "A", [][]uint8{[]uint8("x"), []uint8("y")}); err != nil {
		t.Fatal(err)
	}
	requireEntries(t, fh, []TupleStringListU8{entry("B", "1"), entry("b", "3"), entry("A", "x"), entry("A", "y")})

	clone, err := fh.Clone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	requireEntries(t, fh, []TupleStringListU8{entry("A", "x"), entry("A", "y")})
	requireEntries(t, clone, []TupleStringListU8{entry("B", "1"), entry("b", "3"), entry("A", "x"), entry("A", "y")})
}

func TestFieldsFromHeaderOrdersByName(t *testing.T) {
	h := http.Header{"X-B": {"1", "2"}, "A": {"3"}, "M": {"4"}}
	for range 32 {
		requireEntries(t, NewFieldsHandle(fieldsFromHeader(h, true)), []TupleStringListU8{
			entry("A", "3"), entry("M", "4"), entry("X-B", "1"), entry("X-B", "2"),
		})
	}
}

type foreignFields struct{ *fieldsImpl }

func TestOutgoingRequestKeepsHeaderEntries(t *testing.T) {
	ctx := context.Background()
	list := []TupleStringListU8{entry("x-b", "one"), entry("Accept", "two"), entry("X-B", "three")}
	for name, fh := range map[string]*FieldsHandle{
		"local":   NewFieldsHandle(newFieldsImpl(list, false)),
		"foreign": NewFieldsHandle(foreignFields{newFieldsImpl(list, false)}),
	} {
		t.Run(name, func(t *testing.T) {
			rh, err := (&impl{}).NewOutgoingRequest(ctx, fh)
			if err != nil {
				t.Fatal(err)
			}
			local, _ := rh.LocalImpl()
			req, ok := local.(*outgoingRequestImpl)
			if !ok {
				t.Fatalf("request impl = %T", local)
			}
			headers, err := req.Headers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			requireEntries(t, headers, list)
			if want := (http.Header{"X-B": {"one", "three"}, "Accept": {"two"}}); !reflect.DeepEqual(req.r.Header, want) {
				t.Fatalf("http header = %v, want %v", req.r.Header, want)
			}
		})
	}
}

var (
	invalidSyntax types.HeaderError = types.HeaderErrorInvalidSyntax{}
	forbidden     types.HeaderError = types.HeaderErrorForbidden{}
	immutable     types.HeaderError = types.HeaderErrorImmutable{}
)

func headerErr(t *testing.T) func(Result_HeaderError, error) types.HeaderError {
	return func(res Result_HeaderError, err error) types.HeaderError {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		switch r := res.(type) {
		case types.Result_HeaderErrorOk:
			return nil
		case Result_HeaderErrorErr:
			return r.Value
		}
		t.Fatalf("result = %#v", res)
		return nil
	}
}

func fromListErr(t *testing.T, list ...TupleStringListU8) types.HeaderError {
	t.Helper()
	res, err := (&impl{}).FieldsFromList(context.Background(), list)
	if err != nil {
		t.Fatal(err)
	}
	switch r := res.(type) {
	case types.ResultFieldsHeaderErrorOk:
		return nil
	case types.ResultFieldsHeaderErrorErr:
		return r.Value
	}
	t.Fatalf("from-list = %#v", res)
	return nil
}

func TestFieldsForbiddenNamesMatchWasmtime(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"Connection", "KEEP-ALIVE", "proxy-authenticate", "Proxy-Authorization", "proxy-connection", "Transfer-Encoding", "upgrade", "Host", "HTTP2-Settings"} {
		t.Run(name, func(t *testing.T) {
			if got := fromListErr(t, entry("accept", "a"), entry(name, "v")); got != forbidden {
				t.Fatalf("from-list = %#v, want forbidden", got)
			}
			fh := NewFieldsHandle(newFieldsImpl([]TupleStringListU8{entry("accept", "a")}, false))
			for op, got := range map[string]types.HeaderError{
				"append": headerErr(t)(fh.Append(ctx, name, []uint8("v"))),
				"set":    headerErr(t)(fh.Set(ctx, name, [][]uint8{[]uint8("v")})),
				"delete": headerErr(t)(fh.Delete(ctx, name)),
			} {
				if got != forbidden {
					t.Fatalf("%s = %#v, want forbidden", op, got)
				}
			}
			requireEntries(t, fh, []TupleStringListU8{entry("accept", "a")})
		})
	}
	for _, e := range []TupleStringListU8{entry("Content-Length", "5"), entry("te", "trailers"), entry("Trailer", "x-t")} {
		if got := fromListErr(t, e); got != nil {
			t.Fatalf("from-list %q = %#v, want ok", e.F0, got)
		}
		fh := NewFieldsHandle(newFieldsImpl(nil, false))
		if got := headerErr(t)(fh.Append(ctx, e.F0, e.F1)); got != nil {
			t.Fatalf("append %q = %#v, want ok", e.F0, got)
		}
	}
}

func TestFieldsHeaderErrorOrder(t *testing.T) {
	ctx := context.Background()
	mutable := func() *FieldsHandle { return NewFieldsHandle(newFieldsImpl(nil, false)) }
	frozen := func() *FieldsHandle { return NewFieldsHandle(newFieldsImpl(nil, true)) }
	for _, c := range []struct {
		name   string
		fields func() *FieldsHandle
		key    string
		value  string
		append types.HeaderError
		set    types.HeaderError
		delete types.HeaderError
	}{
		{"invalid name on immutable", frozen, "bad header", "v", invalidSyntax, invalidSyntax, invalidSyntax},
		{"forbidden name on immutable", frozen, "host", "v", forbidden, forbidden, forbidden},
		{"invalid value on immutable", frozen, "x-ok", "bad\nvalue", invalidSyntax, immutable, immutable},
		{"valid on immutable", frozen, "x-ok", "v", immutable, immutable, immutable},
		{"invalid name and forbidden-looking value", mutable, "host name", "v", invalidSyntax, invalidSyntax, invalidSyntax},
		{"forbidden name with invalid value", mutable, "connection", "bad\nvalue", forbidden, forbidden, forbidden},
		{"valid", mutable, "x-ok", "v", nil, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := headerErr(t)(c.fields().Append(ctx, c.key, []uint8(c.value))); got != c.append {
				t.Errorf("append = %#v, want %#v", got, c.append)
			}
			if got := headerErr(t)(c.fields().Set(ctx, c.key, [][]uint8{[]uint8(c.value)})); got != c.set {
				t.Errorf("set = %#v, want %#v", got, c.set)
			}
			if got := headerErr(t)(c.fields().Delete(ctx, c.key)); got != c.delete {
				t.Errorf("delete = %#v, want %#v", got, c.delete)
			}
		})
	}
}

func TestFieldsSyntax(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"", "bad header", "a:b", "a\nb", "a\r", "caf\xc3\xa9", "(x)", "a/b", "{x}", "a\"b"} {
		if got := fromListErr(t, entry(name, "v")); got != invalidSyntax {
			t.Errorf("from-list name %q = %#v, want invalid-syntax", name, got)
		}
		fh := NewFieldsHandle(newFieldsImpl([]TupleStringListU8{entry("x-ok", "v")}, false))
		if got, _ := fh.Get(ctx, name); len(got) != 0 {
			t.Errorf("get %q = %q, want none", name, got)
		}
		if has, _ := fh.Has(ctx, name); has {
			t.Errorf("has %q = true", name)
		}
	}
	for _, name := range []string{"x-ok", "X_Ok", "!#$%&'*+-.^_`|~09azAZ"} {
		if got := fromListErr(t, entry(name, "v")); got != nil {
			t.Errorf("from-list name %q = %#v, want ok", name, got)
		}
	}
	for _, value := range []string{"bad\nvalue", "a\rb", "\x00", "a\x1fb", "a\x7fb"} {
		if got := fromListErr(t, entry("x-ok", value)); got != invalidSyntax {
			t.Errorf("from-list value %q = %#v, want invalid-syntax", value, got)
		}
		fh := NewFieldsHandle(newFieldsImpl(nil, false))
		if got := headerErr(t)(fh.Set(ctx, "x-ok", [][]uint8{[]uint8("fine"), []uint8(value)})); got != invalidSyntax {
			t.Errorf("set value %q = %#v, want invalid-syntax", value, got)
		}
		requireEntries(t, fh, []TupleStringListU8{})
	}
	for _, value := range []string{"", "plain", "tab\there", " spaced ", "\x80\xff", "caf\xc3\xa9"} {
		if got := fromListErr(t, entry("x-ok", value)); got != nil {
			t.Errorf("from-list value %q = %#v, want ok", value, got)
		}
	}
	for _, value := range []string{"", "+5", "-5", " 5", "5 ", "0x5", "18446744073709551616"} {
		if got := fromListErr(t, entry("Content-Length", value)); got != invalidSyntax {
			t.Errorf("content-length %q = %#v, want invalid-syntax", value, got)
		}
	}
	fh := NewFieldsHandle(newFieldsImpl(nil, false))
	if got := headerErr(t)(fh.Append(ctx, "content-length", []uint8("007"))); got != nil {
		t.Fatalf("append content-length 007 = %#v", got)
	}
	requireEntries(t, fh, []TupleStringListU8{entry("content-length", "7")})
}

func TestOutboundRequestInvalidHeaderProgram(t *testing.T) {
	ctx := context.Background()
	hdrs, err := (&impl{}).NewFields(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, got, want types.HeaderError) {
		t.Helper()
		if got != want {
			t.Fatalf("%s = %#v, want %#v", what, got, want)
		}
	}
	check(`append("malformed header name")`, headerErr(t)(hdrs.Append(ctx, "malformed header name", []uint8("ok value"))), invalidSyntax)
	check(`append("ok-header-name")`, headerErr(t)(hdrs.Append(ctx, "ok-header-name", []uint8("ok value"))), nil)
	check(`append("ok-header-name", "bad\nvalue")`, headerErr(t)(hdrs.Append(ctx, "ok-header-name", []uint8("bad\nvalue"))), invalidSyntax)
	check(`append("Connection")`, headerErr(t)(hdrs.Append(ctx, "Connection", []uint8("keep-alive"))), forbidden)
	check(`append("Keep-Alive")`, headerErr(t)(hdrs.Append(ctx, "Keep-Alive", []uint8("stuff"))), forbidden)
	check(`append("Host")`, headerErr(t)(hdrs.Append(ctx, "Host", []uint8("example.com"))), forbidden)
	check(`from-list("bad header")`, fromListErr(t, entry("bad header", "value")), invalidSyntax)
	check(`from-list("ok-header-name", "bad\nvalue")`, fromListErr(t, entry("ok-header-name", "bad\nvalue")), invalidSyntax)
	check(`append("Content-Length", "5")`, headerErr(t)(hdrs.Append(ctx, "Content-Length", []uint8("5"))), nil)
	check(`append("Content-Length", "6")`, headerErr(t)(hdrs.Append(ctx, "Content-Length", []uint8("6"))), invalidSyntax)
	check(`append("content-length", "5")`, headerErr(t)(hdrs.Append(ctx, "content-length", []uint8("5"))), invalidSyntax)
	check(`set("Content-Length", ["5", "6"])`, headerErr(t)(hdrs.Set(ctx, "Content-Length", [][]uint8{[]uint8("5"), []uint8("6")})), invalidSyntax)
	check(`set("Content-Length", ["10"])`, headerErr(t)(hdrs.Set(ctx, "Content-Length", [][]uint8{[]uint8("10")})), nil)
	check(`delete("Content-Length")`, headerErr(t)(hdrs.Delete(ctx, "Content-Length")), nil)
	check(`from-list(Content-Length x2)`, fromListErr(t, entry("Content-Length", "5"), entry("Content-Length", "6")), invalidSyntax)

	req, err := (&impl{}).NewOutgoingRequest(ctx, hdrs)
	if err != nil {
		t.Fatal(err)
	}
	reqHeaders, err := req.Headers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(`request set`, headerErr(t)(reqHeaders.Set(ctx, "Content-Length", [][]uint8{[]uint8("10")})), immutable)
	check(`request append`, headerErr(t)(reqHeaders.Append(ctx, "Content-Length", []uint8("10"))), immutable)
	check(`request delete`, headerErr(t)(reqHeaders.Delete(ctx, "Content-Length")), immutable)
}

func TestIncomingHeadersDropForbiddenNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("X-Ok", "1")
		h.Add("X-Multi", "a")
		h.Add("X-Multi", "b")
		h.Set("Proxy-Authenticate", "Basic")
		h.Set("Http2-Settings", "AAMAAABkAAQAoAAAAAIAAAAA")
		h.Set("Keep-Alive", "timeout=5")
		h.Set("Upgrade", "h2c")
		h.Set("Te", "trailers")
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, name := range []string{"Proxy-Authenticate", "Http2-Settings", "Keep-Alive", "Upgrade"} {
		if resp.Header.Get(name) == "" {
			t.Fatalf("response lacks %s, so the test checks nothing: %v", name, resp.Header)
		}
	}

	ctx := context.Background()
	headers, err := NewIncomingResponse(resp, nil, nil, nil).Headers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := headers.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if isForbiddenHeader(e.F0) {
			t.Fatalf("entries include forbidden %q: %q", e.F0, entries)
		}
	}
	for _, name := range []string{"X-Ok", "X-Multi", "Te"} {
		if has, _ := headers.Has(ctx, name); !has {
			t.Fatalf("entries lack %s: %q", name, entries)
		}
	}

	clone, err := headers.Clone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireEntries(t, clone, entries)

	res, err := (&impl{}).FieldsFromList(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(types.ResultFieldsHeaderErrorOk)
	if !isOK {
		t.Fatalf("from-list(entries) = %#v", res)
	}
	requireEntries(t, ok.Value, entries)
}
