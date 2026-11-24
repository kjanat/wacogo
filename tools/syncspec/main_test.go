package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReadCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "UPSTREAM.txt")
	writeTestFile(t, path, "repo: x\ncommit:  abc123 \n")
	if got, err := readCommit(path); err != nil || got != "abc123" {
		t.Fatalf("readCommit = %q, %v", got, err)
	}
	writeTestFile(t, path, "repo: x\n")
	if _, err := readCommit(path); err == nil || !strings.Contains(err.Error(), "no commit") {
		t.Fatalf("readCommit without commit = %v", err)
	}
	if _, err := readCommit(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("readCommit missing = %v", err)
	}
}

type archiveTransport struct {
	status  int
	archive []byte
	url     string
}

func (a *archiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	a.url = req.URL.String()
	return &http.Response{StatusCode: a.status, Body: io.NopCloser(bytes.NewReader(a.archive)), Request: req}, nil
}

func TestRunDownloadsPinnedCommit(t *testing.T) {
	saved := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = saved })

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "UPSTREAM.txt"), "commit: c1\n")
	transport := &archiveTransport{status: http.StatusOK, archive: testArchive(t)}
	http.DefaultClient.Transport = transport
	if err := run(dir); err != nil {
		t.Fatal(err)
	}
	if want := "https://codeload.github.com/WebAssembly/component-model/tar.gz/c1"; transport.url != want {
		t.Fatalf("url = %q, want %q", transport.url, want)
	}
	if got := readTestFile(t, filepath.Join(dir, "two.wast")); got != "two" {
		t.Fatalf("two.wast = %q", got)
	}

	transport.status = http.StatusNotFound
	if err := run(dir); err == nil || err.Error() != "download: HTTP 404" {
		t.Fatalf("run with 404 = %v", err)
	}
	if err := run(filepath.Join(dir, "missing")); err == nil || !strings.HasPrefix(err.Error(), "read UPSTREAM.txt:") {
		t.Fatalf("run without UPSTREAM.txt = %v", err)
	}
}

type testEntry struct{ name, body string }

func testArchive(t *testing.T, extra ...testEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	entries := []testEntry{
		{"component-model-c1/test/a/one.wast", "one"},
		{"component-model-c1/test/two.wast", "two"},
		{"component-model-c1/test/notes.md", "skip"},
		{"component-model-c1/design/x.wast", "skip"},
	}
	for _, entry := range append(entries, extra...) {
		name, body := entry.name, entry.body
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "component-model-c1/test/dir.wast/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestExtractReplacesMirror(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "UPSTREAM.txt"), "commit: c1\n")
	writeTestFile(t, filepath.Join(dir, "skip.json"), "{}")
	writeTestFile(t, filepath.Join(dir, "old", "stale.wast"), "stale")

	count, err := extract(bytes.NewReader(testArchive(t)), "c1", dir)
	if err != nil || count != 2 {
		t.Fatalf("extract = %d, %v", count, err)
	}
	if got := readTestFile(t, filepath.Join(dir, "a", "one.wast")); got != "one" {
		t.Fatalf("one.wast = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "two.wast")); got != "two" {
		t.Fatalf("two.wast = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "skip.json")); got != "{}" {
		t.Fatalf("skip.json = %q", got)
	}
	if got := readTestFile(t, filepath.Join(dir, "UPSTREAM.txt")); got != "commit: c1\n" {
		t.Fatalf("UPSTREAM.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Fatalf("stale mirror entry kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("non-wast file written: %v", err)
	}
}

func TestExtractDoesNotOverwriteDuplicate(t *testing.T) {
	dir := t.TempDir()
	archive := testArchive(t, testEntry{"component-model-c1/test/a/one.wast", "replacement"})
	count, err := extract(bytes.NewReader(archive), "c1", dir)
	if !errors.Is(err, os.ErrExist) || count != 2 {
		t.Fatalf("extract duplicate = %d, %v", count, err)
	}
	if got := readTestFile(t, filepath.Join(dir, "a", "one.wast")); got != "one" {
		t.Fatalf("duplicate replaced original: %q", got)
	}
}

func TestExtractErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := extract(strings.NewReader("not gzip"), "c1", dir); err == nil || !strings.HasPrefix(err.Error(), "gunzip:") {
		t.Fatalf("extract non-gzip = %v", err)
	}

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	if _, err := gz.Write([]byte("not a tar archive, but long enough to fill a header block ................................................................................................................................................................................................................................................................................................................................................................................................................................................................................................")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extract(&archive, "c1", dir); err == nil || !strings.HasPrefix(err.Error(), "tar:") {
		t.Fatalf("extract non-tar = %v", err)
	}

	if _, err := extract(strings.NewReader(""), "c1", filepath.Join(dir, "missing")); err == nil {
		t.Fatal("extract into missing dir succeeded")
	}
}

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func TestWriteFileRejectsInvalidNames(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)
	for _, name := range []string{"dir/../case.wast", "/case.wast", `dir\case.wast`, "C:case.wast"} {
		if err := writeFile(root, name, strings.NewReader("")); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("mirror after invalid names: %v, %v", entries, err)
	}
}

func TestWriteFileConfinesDirectoryLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	root := openTestRoot(t, dir)
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	if err := writeFile(root, "linked/nested/case.wast", strings.NewReader("")); err == nil {
		t.Fatal("accepted a directory link outside the mirror")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
}

func TestWriteFileErrors(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)
	blocker := filepath.Join(dir, "file")
	writeTestFile(t, blocker, "x")
	if err := writeFile(root, "file/sub/a.wast", strings.NewReader("a")); err == nil || !strings.HasPrefix(err.Error(), "mkdir ") {
		t.Fatalf("writeFile under file = %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a.wast"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "a.wast", strings.NewReader("a")); err == nil || !strings.HasPrefix(err.Error(), "create ") {
		t.Fatalf("writeFile onto dir = %v", err)
	}
	if err := writeFile(root, "b.wast", iotest.ErrReader(os.ErrClosed)); err == nil || !strings.HasPrefix(err.Error(), "write ") {
		t.Fatalf("writeFile with failing reader = %v", err)
	}
}
