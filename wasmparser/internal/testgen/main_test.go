package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordingConverter struct {
	paths []string
	err   error
}

func (r *recordingConverter) JsonFromWast(_ context.Context, wastPath, _ string) error {
	r.paths = append(r.paths, wastPath)
	return r.err
}

func TestGenerateConvertsWastFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.wast", "a.wast", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.wast"), 0o755); err != nil {
		t.Fatal(err)
	}

	conv := &recordingConverter{}
	if err := generate(context.Background(), conv, dir, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, "a.wast"), filepath.Join(dir, "b.wast")}; !reflect.DeepEqual(conv.paths, want) {
		t.Fatalf("converted %q, want %q", conv.paths, want)
	}

	failure := errors.New("convert")
	if err := generate(context.Background(), &recordingConverter{err: failure}, dir, t.TempDir()); !errors.Is(err, failure) {
		t.Fatalf("generate with failing converter = %v", err)
	}
	if err := generate(context.Background(), conv, filepath.Join(dir, "missing"), t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "reading wast dir:") {
		t.Fatalf("generate with missing dir = %v", err)
	}
}

func TestRunRejectsUncreatableOutputDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), t.TempDir(), filepath.Join(blocker, "out")); err == nil || !strings.HasPrefix(err.Error(), "creating output dir:") {
		t.Fatalf("run = %v", err)
	}
}
