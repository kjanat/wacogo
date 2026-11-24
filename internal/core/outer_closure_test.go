package core

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

func aliasDAG(levels int, level func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= levels; i++ {
		b.WriteString(level(i))
	}
	return b.String()
}

func twoAliases(scope string, i int) string {
	return fmt.Sprintf(`(alias outer %s $C%d (component $a)) (alias outer %s $C%d (component $b))`, scope, i-1, scope, i-1)
}

func capturingRoot(levels int, body string) string {
	return `(component $Root
  (component $Outer
    (import "m" (core module $M))
    (component $C0 (alias outer $Outer $M (core module)))` + body + fmt.Sprintf(`
    (instance (instantiate $C%d))
    (export "c" (component $C%d)))
  (core module $m)
  (instance $o (instantiate $Outer (with "m" (core module $m))))
  (alias export $o "c" (component $c))
  (instance (instantiate $c)))`, levels, levels)
}

func heapObjectBytes() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()
}

func loadAndInstantiateWithin(t *testing.T, bin []byte, heapLimit uint64, deadline time.Duration) {
	t.Helper()
	ctx := context.Background()
	engine := NewEngine(ctx)
	defer engine.Close(ctx)
	runtime.GC()
	base := heapObjectBytes()
	done := make(chan error, 1)
	go func() {
		comp, err := engine.LoadComponent(ctx, bytes.NewReader(bin))
		if err != nil {
			done <- err
			return
		}
		inst, err := comp.Instantiate(ctx)
		if err == nil {
			err = inst.Close(ctx)
		}
		done <- err
	}()
	timeout := time.After(deadline)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-tick.C:
			if heap := heapObjectBytes(); heap > base+heapLimit {
				panic(fmt.Sprintf("a %d byte component grew the heap by %d MiB", len(bin), (heap-base)>>20))
			}
		case <-timeout:
			panic(fmt.Sprintf("a %d byte component did not load and instantiate within %v", len(bin), deadline))
		}
	}
}

func TestOuterClosureDAGsShareComponents(t *testing.T) {
	const levels = 40
	for name, wat := range map[string]string{
		"defined components": `(component $Root
  (component $C0)` + aliasDAG(levels, func(i int) string {
			return fmt.Sprintf("\n  (component $C%d %s)", i, twoAliases("$Root", i))
		}) + fmt.Sprintf(`
  (instance (instantiate $C%d)))`, levels),
		"components capturing an import": capturingRoot(levels, aliasDAG(levels, func(i int) string {
			return fmt.Sprintf("\n    (component $C%d %s)", i, twoAliases("$Outer", i))
		})),
		"nested components capturing an import": capturingRoot(levels, aliasDAG(levels, func(i int) string {
			return fmt.Sprintf("\n    (component $C%d (component $I %s) (component $J %s) (instance (instantiate $I)) (instance (instantiate $J)))", i, twoAliases("$Outer", i), twoAliases("$Outer", i))
		})),
	} {
		t.Run(name, func(t *testing.T) {
			bin := buildComponentBytes(t, wat)
			if len(bin) > 8<<10 {
				t.Fatalf("component is %d bytes, want a few KB", len(bin))
			}
			loadAndInstantiateWithin(t, bin, 64<<20, 30*time.Second)
		})
	}
}

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
