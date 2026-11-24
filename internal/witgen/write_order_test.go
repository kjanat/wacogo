package witgen

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGenerate_WriteErrorNamesFirstPath(t *testing.T) {
	opts := Options{
		WitPath:     "testdata/wit/add.wit",
		World:       "example:demo/arith",
		OutDir:      t.TempDir(),
		PackageRoot: "example.test/gen",
		DryRun:      true,
	}
	files, err := generateCached(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	paths := slices.Sorted(maps.Keys(files))
	if len(paths) < 2 {
		t.Fatalf("want several files, got %v", paths)
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Join(opts.OutDir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	opts.DryRun = false
	want := "write " + filepath.Join(opts.OutDir, paths[0]) + ":"
	for range 32 {
		if _, err := generateCached(t, opts); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("want prefix %q, got %v", want, err)
		}
	}
}
