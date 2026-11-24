package wasmparser

import "testing"

const deterministicRuns = 32

func requireSameError(t *testing.T, want string, check func() error) {
	t.Helper()
	for range deterministicRuns {
		err := check()
		if err == nil {
			t.Fatalf("want %q, got nil", want)
		}
		if err.Error() != want {
			t.Fatalf("want %q, got %q", want, err.Error())
		}
	}
}

func TestValidatorErrorsNameFirstDeclaredEntry(t *testing.T) {
	for name, c := range map[string]struct {
		wat  string
		want string
	}{
		"instance export": {
			wat: `(component
  (component $c
    (import "i" (instance
      (export "h" (func)) (export "g" (func)) (export "f" (func)) (export "e" (func))
      (export "d" (func)) (export "c" (func)) (export "b" (func)) (export "a" (func)))))
  (instance $empty)
  (instance (instantiate $c (with "i" (instance $empty)))))`,
			want: "type mismatch for import `i`: missing expected export `h`",
		},
		"module export": {
			wat: `(component
  (component $c
    (import "m" (core module
      (export "h" (func)) (export "g" (func)) (export "f" (func)) (export "e" (func))
      (export "d" (func)) (export "c" (func)) (export "b" (func)) (export "a" (func)))))
  (core module $m)
  (instance (instantiate $c (with "m" (core module $m)))))`,
			want: "type mismatch for import `m`: missing expected export `h`: module export `h` not defined",
		},
		"module import": {
			wat: `(component
  (component $c (import "m" (core module)))
  (core module $m
    (import "x" "h" (func)) (import "x" "g" (func)) (import "x" "f" (func)) (import "x" "e" (func))
    (import "x" "d" (func)) (import "x" "c" (func)) (import "x" "b" (func)) (import "x" "a" (func)))
  (instance (instantiate $c (with "m" (core module $m)))))`,
			want: "type mismatch for import `m`: missing expected import `::h`: module import `x::h` not defined",
		},
		"core instantiation argument": {
			wat: `(component
  (core module $m
    (import "h" "f" (func)) (import "g" "f" (func)) (import "f" "f" (func)) (import "e" "f" (func))
    (import "d" "f" (func)) (import "c" "f" (func)) (import "b" "f" (func)) (import "a" "f" (func)))
  (core instance (instantiate $m)))`,
			want: "missing module instantiation argument named `h`",
		},
	} {
		t.Run(name, func(t *testing.T) {
			bin := wat2wasm(t, c.wat)
			requireSameError(t, c.want, func() error { return validateAll(bin) })
		})
	}
}

func TestCheckInstantiationNamesFirstDeclaredExport(t *testing.T) {
	consumer := loadComponent(t, `(component
  (import "i" (instance
    (export "h" (func)) (export "g" (func)) (export "f" (func)) (export "e" (func))
    (export "d" (func)) (export "c" (func)) (export "b" (func)) (export "a" (func)))))`)
	lender := testInstance(t, loadComponent(t, `(component)`))
	requireSameError(t, "import \"i\": export `h` was not found", func() error {
		return consumer.CheckInstantiation(map[string]any{"i": lender})
	})
}
