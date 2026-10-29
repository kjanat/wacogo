package witgen

import (
	"strings"
	"testing"
)

// A component importing an interface can name its primitive aliases, so
// the host factory must export each one while uses stay inline.
func TestGenerate_ExportsPrimitiveAliases(t *testing.T) {
	out, err := generateCached(t, Options{
		WitPath:     "testdata/wit/primitive_alias.wit",
		World:       "example:demo/idshost",
		OutDir:      t.TempDir(),
		PackageRoot: "example.com/test",
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var content string
	for path, data := range out {
		if strings.HasSuffix(path, ".bind.go") {
			content = string(data)
		}
	}
	if content == "" {
		t.Fatal("no .bind.go file emitted")
	}
	for _, want := range []string{
		`b_.AddType("occurrence-id", host.String)`,
		`b_.AddType("count", host.U32)`,
		`{Name: "id", Type: host.String}`,
		`{Name: "n", Type: host.U32}`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in generated factory:\n%s", want, content)
		}
	}
}
