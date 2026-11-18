package wasmparser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// tupleComponent encodes a component defining a tuple of size u32s, by hand since
// wasm-tools won't encode past its own limits. With nested set, the tuple is in a
// component inside it.
func tupleComponent(size int, nested bool) []byte {
	section := func(id byte, body []byte) []byte {
		return append(binary.AppendUvarint([]byte{id}, uint64(len(body))), body...)
	}
	header := []byte{0x00, 'a', 's', 'm', 0x0d, 0x00, 0x01, 0x00}

	types := binary.AppendUvarint([]byte{1, 0x6f}, uint64(size)) // one type: a tuple
	types = append(types, bytes.Repeat([]byte{0x79}, size)...)   // of u32
	inner := append(bytes.Clone(header), section(7, types)...)
	if !nested {
		return inner
	}
	return append(bytes.Clone(header), section(4, inner)...)
}

// validateAll feeds ParseAll to a separate Validator, as its docs describe.
func validateAll(bin []byte, opts ...ParseOption) error {
	v := NewValidator(DefaultFeatures(), opts...)
	for p, err := range ParseAll(bytes.NewReader(bin), opts...) {
		if err != nil {
			return err
		}
		if err := v.ValidatePayload(p); err != nil {
			return err
		}
	}
	return nil
}

func TestParseAll_NestingDepthAcrossSections(t *testing.T) {
	opts := WithParseLimits(ParseLimits{NestingDepth: 1})
	for _, wat := range []string{
		`(component (type (instance)))`,
		`(component (component))`,
	} {
		if err := validateAll(wat2wasm(t, wat), opts); err != nil {
			t.Errorf("%s: %v", wat, err)
		}
	}
	bin := wat2wasm(t, `(component (component (type (instance))))`)
	if err := validateAll(bin, opts); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("nested instance type: got %v, want a depth error", err)
	}
}

func TestParseAll_CoreModuleLimitsCountImports(t *testing.T) {
	for _, tc := range []struct {
		name, imported, defined string
		limits                  ParseLimits
	}{
		{"func", `(func (import "m" "i"))`, `(func)`, ParseLimits{Functions: 1}},
		{"table", `(table (import "m" "i") 1 funcref)`, `(table 1 funcref)`, ParseLimits{Tables: 1}},
		{"memory", `(memory (import "m" "i") 1)`, `(memory 1)`, ParseLimits{Memories: 1}},
		{"global", `(global (import "m" "i") i32)`, `(global i32 (i32.const 0))`, ParseLimits{Globals: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := WithParseLimits(tc.limits)
			within := wat2wasm(t, "(component (core module "+tc.imported+"))")
			if err := validateAll(within, opt); err != nil {
				t.Fatalf("one imported %s: %v", tc.name, err)
			}
			over := wat2wasm(t, "(component (core module "+tc.imported+tc.defined+"))")
			if err := validateAll(over, opt); err == nil || !strings.Contains(err.Error(), tc.name+" count") {
				t.Fatalf("combined %s count: got %v", tc.name, err)
			}
		})
	}
}

// TestParseAll_Limits checks limits given to ParseAll reach nested parsers and
// section contents, in both directions.
func TestParseAll_Limits(t *testing.T) {
	for _, nested := range []bool{false, true} {
		over := tupleComponent(10_001, nested)
		if err := validateAll(over); err == nil || !strings.Contains(err.Error(), "tuple") {
			t.Errorf("nested=%v, default limits: got %v", nested, err)
		}
		if err := validateAll(over, WithParseLimits(ParseLimits{TupleTypes: 20_000})); err != nil {
			t.Errorf("nested=%v, raised: %v", nested, err)
		}

		small := tupleComponent(2, nested)
		if err := validateAll(small); err != nil {
			t.Errorf("nested=%v, within the default: %v", nested, err)
		}
		if err := validateAll(small, WithParseLimits(ParseLimits{TupleTypes: 1})); err == nil {
			t.Errorf("nested=%v: lowered limit not applied", nested)
		}
	}
}

// TestParseAll_InstanceLimits checks limits a Validator enforces still reach it
// from the same options.
func TestParseAll_InstanceLimits(t *testing.T) {
	// A component whose instance section instantiates its one nested component twice.
	section := func(id byte, body []byte) []byte {
		return append(binary.AppendUvarint([]byte{id}, uint64(len(body))), body...)
	}
	header := []byte{0x00, 'a', 's', 'm', 0x0d, 0x00, 0x01, 0x00}
	bin := append(bytes.Clone(header), section(4, header)...)
	bin = append(bin, section(5, []byte{2, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})...)

	if err := validateAll(bin); err != nil {
		t.Fatal(err)
	}
	err := validateAll(bin, WithParseLimits(ParseLimits{ComponentInstances: 2}))
	if !errors.Is(err, ErrInstanceLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

// TestParseLimits_Resolved checks that every field has a default, so a limit
// left unset can't mean rejecting everything, and that every field can be
// overridden.
func TestParseLimits_Resolved(t *testing.T) {
	const override = 12345

	var set ParseLimits
	rv := reflect.ValueOf(&set).Elem()
	for i := range rv.NumField() {
		rv.Field(i).SetUint(override)
	}
	resolved := reflect.ValueOf(set.resolved())
	defaults := reflect.ValueOf(ParseLimits{}.resolved())
	for i := range rv.NumField() {
		name := rv.Type().Field(i).Name
		if got := resolved.Field(i).Uint(); got != override {
			t.Errorf("%s: override gave %d", name, got)
		}
		if defaults.Field(i).Uint() == 0 {
			t.Errorf("%s has no default", name)
		}
	}
}
