package core

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestCoreModuleTablesExcludeBorrowedImports(t *testing.T) {
	data := buildComponentBytes(t, `(module (import "host" "table" (table 2 3 funcref)) (table 1 4 funcref) (table 0 externref))`)
	tables, err := CoreModuleTables(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || tables[0].Min != 1 || !tables[0].Max.Valid || tables[0].Max.Value != 4 || tables[1].Min != 0 || tables[1].Max.Valid {
		t.Fatalf("defined tables = %+v", tables)
	}
	if _, err := CoreModuleTables(buildComponentBytes(t, `(component)`)); err == nil {
		t.Fatal("component accepted as a core module")
	}
	if _, err := CoreModuleTables([]byte("invalid")); err == nil {
		t.Fatal("invalid module accepted")
	}
}

type tableAdmission struct {
	limit, live, calls, releases int
}

var errTableAdmission = errors.New("table admission refused")

func (a *tableAdmission) allocate(tables []CoreTable) (func(), error) {
	a.calls++
	if len(tables) != 1 || !tables[0].Max.Valid || a.live == a.limit {
		return nil, errTableAdmission
	}
	a.live++
	return func() { a.live--; a.releases++ }, nil
}

const repeatedTableComponent = `(component
  (core module $owner (table (export "table") 1 2 funcref))
  (core instance $one (instantiate $owner))
  (core instance (instantiate $owner))
  (core module $borrower (import "owner" "table" (table 1 2 funcref)))
  (core instance (instantiate $borrower (with "owner" (instance $one))))
  (component $child
    (core module $owner (table 1 2 funcref))
    (core instance (instantiate $owner)))
  (instance (instantiate $child))
  (instance (instantiate $child)))`

func TestCoreTableAdmissionCountsRepeatedAndNestedInstances(t *testing.T) {
	for _, limit := range []int{3, 4} {
		t.Run(map[int]string{3: "refused", 4: "accepted"}[limit], func(t *testing.T) {
			admission := &tableAdmission{limit: limit}
			ctx := WithCoreTableAllocator(t.Context(), admission.allocate)
			engine := NewEngine(ctx)
			t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(ctx)) })
			component, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, repeatedTableComponent)))
			if err != nil {
				t.Fatal(err)
			}
			instance, err := component.Instantiate(ctx)
			if limit == 3 {
				if !errors.Is(err, errTableAdmission) || instance != nil || admission.live != 0 || admission.releases != 3 {
					t.Fatalf("failed nested admission leaked: instance=%v err=%v state=%+v", instance, err, admission)
				}
			} else {
				if err != nil || admission.live != 4 {
					t.Fatalf("accepted allocation: err=%v state=%+v", err, admission)
				}
				for range 2 {
					if err := instance.Close(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if admission.live != 0 || admission.releases != 4 {
					t.Fatalf("reservation not released once: %+v", admission)
				}
			}
			if admission.calls != 4 {
				t.Fatalf("borrowed table charged or nested instance missed: %+v", admission)
			}
		})
	}
}

func TestCoreTableAdmissionRollsBackStartTrap(t *testing.T) {
	admission := &tableAdmission{limit: 1}
	ctx := WithCoreTableAllocator(t.Context(), admission.allocate)
	engine := NewEngine(ctx)
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(ctx)) })
	component, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, `(component
  (core module $m (table 0 2 funcref) (func $start unreachable) (start $start))
  (core instance (instantiate $m)))`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := component.Instantiate(ctx); err == nil || admission.live != 0 || admission.releases != 1 {
		t.Fatalf("start failure leaked table reservation: err=%v state=%+v", err, admission)
	}
}

func TestCoreTableAdmissionRetainsBoundedGrowth(t *testing.T) {
	admission := &tableAdmission{limit: 1}
	ctx := WithCoreTableAllocator(t.Context(), admission.allocate)
	engine := NewEngine(ctx)
	t.Cleanup(func() { _ = engine.Close(context.WithoutCancel(ctx)) })
	component, err := engine.LoadComponent(ctx, bytes.NewReader(buildComponentBytes(t, `(component
  (core module $m (table 0 2 funcref)
    (func $start
      ref.null func i32.const 2 table.grow i32.const 0 i32.ne if unreachable end
      ref.null func i32.const 1 table.grow i32.const -1 i32.ne if unreachable end)
    (start $start))
  (core instance (instantiate $m)))`)))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := component.Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(ctx); err != nil || admission.live != 0 || admission.releases != 1 {
		t.Fatalf("bounded growth changed allocation ownership: err=%v state=%+v", err, admission)
	}
}
