package catalog

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCatalogFileRejectsDecompressionBomb(t *testing.T) {
	// A catalog can be shared or arrive in an automated refresh pull request,
	// so a highly compressible artifact must not be expanded without a ceiling.
	path := filepath.Join(t.TempDir(), "bomb.json.gz")
	writeGzipFixture(t, path, make([]byte, 8<<20))

	if _, err := readCatalogFile(path, 1<<20); err == nil {
		t.Fatal("expected an error for an artifact past the limit")
	} else if !strings.Contains(err.Error(), "exceeds the") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadCatalogFileAcceptsArtifactAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact.json.gz")
	payload := make([]byte, 1<<20)
	writeGzipFixture(t, path, payload)

	data, err := readCatalogFile(path, int64(len(payload)))
	if err != nil {
		t.Fatalf("artifact exactly at the limit must be accepted: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("got %d bytes, want %d", len(data), len(payload))
	}
}

func TestReadCatalogFileLimitsUncompressedArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.json")
	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCatalogFile(path, 1024); err == nil {
		t.Fatal("expected the limit to apply to uncompressed artifacts too")
	}
}

func TestSafeCatalogPathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"../outside.json",
		"snapshots/../../outside.json",
		"/etc/passwd",
	} {
		if _, err := safeCatalogPath(root, relative); err == nil {
			t.Fatalf("expected %q to be rejected", relative)
		}
	}
}

func TestSafeCatalogPathAcceptsRelativeArtifacts(t *testing.T) {
	root := t.TempDir()
	path, err := safeCatalogPath(root, "snapshots/x/mappings/aws-to-google.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, root) {
		t.Fatalf("resolved path %q escaped root %q", path, root)
	}
}

func TestLoadLatestRejectsUnsupportedFormat(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "snapshots", "x", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(Manifest{FormatVersion: "does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	pointer, err := json.Marshal(LatestPointer{Manifest: "snapshots/x/manifest.json"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "latest.json"), pointer, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadLatest(root); err == nil || !strings.Contains(err.Error(), "unsupported catalog format") {
		t.Fatalf("expected an unsupported-format error, got %v", err)
	}
}

func TestLoadLatestRejectsPointerEscapingRoot(t *testing.T) {
	root := t.TempDir()
	pointer, err := json.Marshal(LatestPointer{Manifest: "../../etc/passwd"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "latest.json"), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLatest(root); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("expected an escape error, got %v", err)
	}
}

func writeGzipFixture(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := gzip.NewWriter(file)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}
