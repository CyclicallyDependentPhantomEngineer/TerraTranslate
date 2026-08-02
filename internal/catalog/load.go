package catalog

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LoadLatest loads and validates the current complete snapshot manifest.
func LoadLatest(catalogDir string) (*Manifest, error) {
	root, err := filepath.Abs(catalogDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, "latest.json"))
	if err != nil {
		return nil, fmt.Errorf("read catalog latest pointer: %w", err)
	}
	var pointer LatestPointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return nil, fmt.Errorf("decode catalog latest pointer: %w", err)
	}
	manifestPath, err := safeCatalogPath(root, pointer.Manifest)
	if err != nil {
		return nil, err
	}
	data, err = os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read catalog manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode catalog manifest: %w", err)
	}
	if manifest.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("unsupported catalog format %q", manifest.FormatVersion)
	}
	return &manifest, nil
}

// LoadMapping loads one generated provider-pair mapping from the latest snapshot.
func LoadMapping(catalogDir, sourceProvider, targetProvider string) (*MappingCatalog, error) {
	manifest, err := LoadLatest(catalogDir)
	if err != nil {
		return nil, err
	}
	key := sourceProvider + "-to-" + targetProvider
	relative, exists := manifest.Mappings[key]
	if !exists {
		return nil, fmt.Errorf("catalog has no mapping for %s -> %s", sourceProvider, targetProvider)
	}
	root, _ := filepath.Abs(catalogDir)
	path, err := safeCatalogPath(root, relative)
	if err != nil {
		return nil, err
	}
	data, err := readCatalogFile(path, maxMappingBytes)
	if err != nil {
		return nil, fmt.Errorf("read catalog mapping: %w", err)
	}
	var mapping MappingCatalog
	if err := json.Unmarshal(data, &mapping); err != nil {
		return nil, fmt.Errorf("decode catalog mapping: %w", err)
	}
	return &mapping, nil
}

// A catalog directory is untrusted input: it can be shared, downloaded, or
// arrive in an automated refresh pull request. Compressed artifacts therefore
// need an explicit decompressed ceiling, since a small archive of repeated
// bytes expands by three orders of magnitude and would otherwise be read
// entirely into memory.
const (
	// Mappings are the translation hot path. The largest real one decompresses
	// to roughly 3.5 MB, so this leaves two orders of magnitude of headroom.
	maxMappingBytes = 256 << 20
	// Provider schemas and Registry module records are far larger; the AWS
	// module artifact decompresses to about 310 MB today.
	maxArtifactBytes = 2 << 30
)

func readCatalogFile(path string, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = maxArtifactBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var source io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		reader, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		source = reader
	}

	// Read one byte past the ceiling so exceeding it is detected rather than
	// silently truncating the artifact into invalid JSON.
	data, err := io.ReadAll(io.LimitReader(source, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("catalog artifact %s exceeds the %d byte limit", filepath.Base(path), limit)
	}
	return data, nil
}

func safeCatalogPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("catalog path must be relative: %q", relative)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("catalog path escapes root: %q", relative)
	}
	return path, nil
}
