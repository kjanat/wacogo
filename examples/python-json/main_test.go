package main

import (
	"os"
	"strings"
	"testing"
)

func TestPythonJSON(t *testing.T) {
	path := os.Getenv("PYTHON_JSON_COMPONENT")
	if path == "" {
		t.Skip("set PYTHON_JSON_COMPONENT to a component built from guest/formatter.py")
	}

	call := func(t *testing.T, source string) ([]byte, error) {
		t.Helper()
		guest, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer guest.Close()
		return formatJSON(t.Context(), guest, []byte(source))
	}

	t.Run("formats UTF-8 JSON", func(t *testing.T) {
		output, err := call(t, `{"name":"café 🦉","valid":true}`)
		if err != nil {
			t.Fatal(err)
		}
		want := "{\n  \"name\": \"café 🦉\",\n  \"valid\": true\n}\n"
		if string(output) != want {
			t.Fatalf("format-json = %q, want %q", output, want)
		}
	})
	t.Run("lifts a guest error without trapping", func(t *testing.T) {
		output, err := call(t, "{")
		if output != nil || err == nil || !strings.Contains(err.Error(), "format-json: Expecting property name") {
			t.Fatalf("format-json = %q, %v; want a Python JSON parse error", output, err)
		}
	})
}
