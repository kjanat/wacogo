// syncspec downloads the WebAssembly/component-model `test/` tree at the
// commit pinned in testdata/spec/UPSTREAM.txt and replaces the contents of
// testdata/spec/ with that tree. Intended to be run from the repo root.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	specDir := flag.String("spec-dir", "testdata/spec", "directory holding UPSTREAM.txt and the mirror")
	flag.Parse()

	if err := run(*specDir); err != nil {
		fmt.Fprintf(os.Stderr, "syncspec: %v\n", err)
		os.Exit(1)
	}
}

func run(specDir string) (err error) {
	commit, err := readCommit(filepath.Join(specDir, "UPSTREAM.txt"))
	if err != nil {
		return fmt.Errorf("read UPSTREAM.txt: %w", err)
	}

	url := fmt.Sprintf("https://codeload.github.com/WebAssembly/component-model/tar.gz/%s", commit)
	fmt.Fprintf(os.Stderr, "fetching %s\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer closeInto(&err, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	count, err := extract(resp.Body, commit, specDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d .wast files into %s\n", count, specDir)
	return nil
}

func closeInto(err *error, c io.Closer) {
	*err = errors.Join(*err, c.Close())
}

func extract(archive io.Reader, commit, specDir string) (count int, err error) {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return 0, fmt.Errorf("gunzip: %w", err)
	}
	defer closeInto(&err, gz)

	if err := clearMirror(specDir); err != nil {
		return 0, fmt.Errorf("clear mirror: %w", err)
	}

	tr := tar.NewReader(gz)
	prefix := fmt.Sprintf("component-model-%s/test/", commit)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel, ok := strings.CutPrefix(hdr.Name, prefix)
		if !ok || !strings.HasSuffix(rel, ".wast") {
			continue
		}
		if err := writeFile(filepath.Join(specDir, rel), tr); err != nil {
			return count, err
		}
		count++
	}
}

func writeFile(dst string, r io.Reader) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	f, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer closeInto(&err, f)
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

func readCommit(path string) (commit string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer closeInto(&err, f)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "commit" {
			return strings.TrimSpace(v), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no commit: line in %s", path)
}

func clearMirror(specDir string) error {
	entries, err := os.ReadDir(specDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "UPSTREAM.txt" || e.Name() == "skip.json" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(specDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
