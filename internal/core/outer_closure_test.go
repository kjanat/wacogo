package core

import (
	"bytes"
	"context"
	"testing"
)

func TestOuterClosuresFollowExportedComponents(t *testing.T) {
	for name, wat := range map[string]string{
		"component": `(component $R
  (component $X (component $Leaf) (export "c" (component $Leaf)))
  (instance $x (instantiate $X))
  (alias export $x "c" (component $C))
  (component $A
    (component $B (alias outer $R $C (component $C2)) (instance (instantiate $C2)))
    (export "b" (component $B)))
  (instance $a (instantiate $A))
  (alias export $a "b" (component $B2))
  (instance (instantiate $B2)))`,
		"core module": `(component $R
  (component $X (core module $Leaf) (export "c" (core module $Leaf)))
  (instance $x (instantiate $X))
  (alias export $x "c" (core module $C))
  (component $A
    (component $B (alias outer $R $C (core module $C2)) (core instance (instantiate $C2)))
    (export "b" (component $B)))
  (instance $a (instantiate $A))
  (alias export $a "b" (component $B2))
  (instance (instantiate $B2)))`,
		"two levels": `(component $R
  (component $X (core module $Leaf) (export "c" (core module $Leaf)))
  (instance $x (instantiate $X))
  (alias export $x "c" (core module $m))
  (component $A
    (component $B
      (component $D (alias outer $R $m (core module $m2)) (core instance (instantiate $m2)))
      (export "d" (component $D)))
    (instance $b (instantiate $B))
    (export "d" (component $b "d")))
  (instance $a (instantiate $A))
  (alias export $a "d" (component $D2))
  (instance (instantiate $D2)))`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			engine := NewEngine(ctx)
			defer engine.Close(ctx)
			comp, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, wat)))
			if err != nil {
				t.Fatal(err)
			}
			defer comp.Close(ctx)
			inst, err := comp.Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := inst.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

const closureModuleX = `(component $X
    (core module $one (func (export "f") (result i32) i32.const 1))
    (export "m" (core module $one)))
  (instance $x (instantiate $X))
  (alias export $x "m" (core module $m))`

const closureComponentC = `(component $C
    (alias outer 1 $m (core module $cm))
    (core instance $i (instantiate $cm))
    (func (export "f") (result u32) (canon lift (core func $i "f"))))`

func TestOuterClosuresKeepDefiningScope(t *testing.T) {
	for name, wat := range map[string]string{
		"instantiation argument": `(component
  ` + closureModuleX + `
  ` + closureComponentC + `
  (component $R
    (import "c" (component $ic (export "f" (func (result u32)))))
    (core module $r0 (func (export "f") (result i32) i32.const 2))
    (instance $ci (instantiate $ic))
    (export "ci" (instance $ci)))
  (instance $r (instantiate $R (with "c" (component $C))))
  (alias export $r "ci" (instance $rci))
  (alias export $rci "f" (func $f))
  (export "f" (func $f)))`,
		"outer alias": `(component
  ` + closureModuleX + `
  ` + closureComponentC + `
  (component $R
    (core module $r0 (func (export "f") (result i32) i32.const 2))
    (alias outer 1 $C (component $c2))
    (instance $ci (instantiate $c2))
    (export "ci" (instance $ci)))
  (instance $r (instantiate $R))
  (alias export $r "ci" (instance $rci))
  (alias export $rci "f" (func $f))
  (export "f" (func $f)))`,
		"inline instance": `(component
  (component $A
    ` + closureModuleX + `
    ` + closureComponentC + `
    (instance $inl (export "c" (component $C)))
    (export "inl" (instance $inl)))
  (core module $p0 (func (export "f") (result i32) i32.const 2))
  (instance $a (instantiate $A))
  (alias export $a "inl" (instance $inl))
  (alias export $inl "c" (component $c))
  (instance $ci (instantiate $c))
  (alias export $ci "f" (func $f))
  (export "f" (func $f)))`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			engine := NewEngine(ctx)
			defer engine.Close(ctx)
			comp, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, wat)))
			if err != nil {
				t.Fatal(err)
			}
			defer comp.Close(ctx)
			inst, err := comp.Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer inst.Close(ctx)
			got, err := inst.ExportedFunc("f").Call(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != ValU32(1) {
				t.Fatalf("f() = %v, want [1]", got)
			}
		})
	}
}
