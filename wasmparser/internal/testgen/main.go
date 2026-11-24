package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/kjanat/wacogo/wasmtools"
)

func main() {
	wastDir := flag.String("wast-dir", "", "directory containing .wast files")
	outDir := flag.String("out-dir", "", "output directory for generated JSON and wasm files")
	flag.Parse()

	if *wastDir == "" || *outDir == "" {
		log.Fatal("both -wast-dir and -out-dir are required")
	}
	if err := run(context.Background(), *wastDir, *outDir); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, wastDir, outDir string) (err error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	tool, err := wasmtools.New(ctx)
	if err != nil {
		return fmt.Errorf("wasmtools.New: %w", err)
	}
	defer func() { err = errors.Join(err, tool.Close(ctx)) }()
	return generate(ctx, tool, wastDir, outDir)
}

type wastConverter interface {
	JsonFromWast(ctx context.Context, wastPath, outDir string) error
}

func generate(ctx context.Context, tool wastConverter, wastDir, outDir string) error {
	entries, err := os.ReadDir(wastDir)
	if err != nil {
		return fmt.Errorf("reading wast dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wast") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".wast")
		fmt.Printf("generating %s...\n", stem)
		if err := tool.JsonFromWast(ctx, filepath.Join(wastDir, entry.Name()), outDir); err != nil {
			return fmt.Errorf("wasm-tools json-from-wast failed for %s: %w", stem, err)
		}
	}
	fmt.Println("done")
	return nil
}
