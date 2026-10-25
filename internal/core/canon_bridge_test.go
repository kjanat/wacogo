package core

import (
	"testing"

	"github.com/kjanat/wacogo/internal/canon"
)

// TestFuncBindingCompiles verifies the bridge produces a compiled call binding
// for a simple (u32) → (u32) signature.
func TestFuncBindingCompiles(t *testing.T) {
	ft := &FuncType{
		Params:  []ParamType{{Name: "x", Type: TypeU32{}}},
		Results: []ResultType{{Type: TypeU32{}}},
	}
	cb := canon.NewCallBinding(
		FuncTypeParamsAsCanon(ft),
		FuncTypeResultsAsCanon(ft),
		canon.Callee{Instance: canonInstanceView{i: &ComponentInstance{}}},
	)
	if cb == nil {
		t.Fatal("NewCallBinding returned nil")
	}
}
