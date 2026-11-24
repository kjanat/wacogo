package types

import (
	"context"
	"net/http"
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

func TestFieldsRejectForbiddenNames(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"Host", "content-length", "CONNECTION", "Transfer-Encoding", "upgrade", "trailer", "te", "keep-alive", "proxy-connection"} {
		t.Run(name, func(t *testing.T) {
			res, err := (&impl{}).FieldsFromList(ctx, []TupleStringListU8{entry("accept", "a"), entry(name, "v")})
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := res.(types.ResultFieldsHeaderErrorErr); !ok || got.Value != (types.HeaderErrorForbidden{}) {
				t.Fatalf("from-list = %#v, want forbidden", res)
			}

			fh := NewFieldsHandle(newFieldsImpl([]TupleStringListU8{entry("accept", "a")}, false))
			for op, call := range map[string]func() (Result_HeaderError, error){
				"append": func() (Result_HeaderError, error) { return fh.Append(ctx, name, []uint8("v")) },
				"set":    func() (Result_HeaderError, error) { return fh.Set(ctx, name, [][]uint8{[]uint8("v")}) },
			} {
				res, err := call()
				if err != nil {
					t.Fatal(err)
				}
				if got, ok := res.(Result_HeaderErrorErr); !ok || got.Value != (types.HeaderErrorForbidden{}) {
					t.Fatalf("%s = %#v, want forbidden", op, res)
				}
			}
			requireEntries(t, fh, []TupleStringListU8{entry("accept", "a")})
		})
	}

	immutable := NewFieldsHandle(newFieldsImpl(nil, true))
	res, err := immutable.Append(ctx, "host", []uint8("v"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := res.(Result_HeaderErrorErr); !ok || got.Value != (types.HeaderErrorImmutable{}) {
		t.Fatalf("append to immutable = %#v, want immutable", res)
	}
}
