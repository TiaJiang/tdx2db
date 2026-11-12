package tdx

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestEnsureOnlineBoardCatalogDownloadsAndReusesETag(t *testing.T) {
	archive := boardCatalogArchive(t)
	var headCalls atomic.Int32
	var getCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"catalog-v1"`)
		writer.Header().Set("Last-Modified", "Wed, 11 Nov 2026 12:50:13 GMT")
		if request.Method == http.MethodHead {
			headCalls.Add(1)
			writer.WriteHeader(http.StatusOK)
			return
		}
		getCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(archive)
	}))
	defer server.Close()
	oldURL := boardCatalogURL
	boardCatalogURL = server.URL
	t.Cleanup(func() { boardCatalogURL = oldURL })

	cacheDir := t.TempDir()
	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 1 || result.Downloads != 1 || headCalls.Load() != 1 || getCalls.Load() != 1 {
		t.Fatalf("unexpected first sync: %+v head=%d get=%d", result, headCalls.Load(), getCalls.Load())
	}
	if !hasBoardCatalog(cacheDir) {
		t.Fatal("board catalog was not written to cache")
	}

	metadata := readBoardCacheMetadata(cacheDir)
	metadata.CatalogCheckedAt = "2000-01-01"
	if err := writeBoardCacheMetadata(cacheDir, metadata); err != nil {
		t.Fatal(err)
	}
	result, err = ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 1 || result.Downloads != 0 || headCalls.Load() != 2 || getCalls.Load() != 1 {
		t.Fatalf("unexpected cached sync: %+v head=%d get=%d", result, headCalls.Load(), getCalls.Load())
	}
}

func TestEnsureOnlineBoardCatalogKeepsCacheWhenDownloadFails(t *testing.T) {
	var headCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headCalls.Add(1)
		writer.Header().Set("ETag", `"catalog-new"`)
		if request.Method == http.MethodHead {
			writer.WriteHeader(http.StatusOK)
			return
		}
		http.Error(writer, "temporary failure", http.StatusInternalServerError)
	}))
	defer server.Close()
	oldURL := boardCatalogURL
	boardCatalogURL = server.URL
	t.Cleanup(func() { boardCatalogURL = oldURL })

	cacheDir := t.TempDir()
	writeCatalogFixture(t, cacheDir)
	metadata := boardCacheMetadata{
		Files:            make(map[string]boardCacheFileMetadata),
		CatalogCheckedAt: "2000-01-01",
		CatalogETag:      `"catalog-old"`,
	}
	if err := writeBoardCacheMetadata(cacheDir, metadata); err != nil {
		t.Fatal(err)
	}

	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 || headCalls.Load() != 2 {
		t.Fatalf("expected warning after failed catalog refresh: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(cacheDir, tdxzsConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("旧目录")) {
		t.Fatalf("cached catalog was replaced: %q", data)
	}
}

func TestEnsureOnlineBoardCatalogUsesExplicitDirectoryWithoutRequests(t *testing.T) {
	overrideDir := t.TempDir()
	writeCatalogFixture(t, overrideDir)
	result, err := ensureOnlineBoardCatalog(context.Background(), t.TempDir(), t.TempDir(), overrideDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 0 || result.Downloads != 0 {
		t.Fatalf("unexpected requests with explicit catalog: %+v", result)
	}
}

func TestEnsureOnlineBoardCatalogReusesDownloadedGBBQ(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "gbbq.zip"), boardCatalogArchive(t), 0644); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, tempDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 0 || result.Downloads != 0 || !hasBoardCatalog(cacheDir) {
		t.Fatalf("shared gbbq.zip was not reused: %+v", result)
	}
}

func boardCatalogArchive(t *testing.T) []byte {
	t.Helper()
	var inner bytes.Buffer
	innerWriter := zip.NewWriter(&inner)
	writeZipFile(t, innerWriter, tdxzsConfigFile, []byte("煤炭|880301|2|1|0|T10\n"))
	writeZipFile(t, innerWriter, tdxzs3ConfigFile, []byte("煤炭|881001|12|1|0|X10\n"))
	if err := innerWriter.Close(); err != nil {
		t.Fatal(err)
	}

	var outer bytes.Buffer
	outerWriter := zip.NewWriter(&outer)
	writeZipFile(t, outerWriter, "zhb.zip", inner.Bytes())
	writeZipFile(t, outerWriter, infoHarborBlockFile,
		[]byte("#GN_概念甲,1,880501,20200101,20260101,,\n0#000001\n"))
	if err := outerWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return outer.Bytes()
}

func writeCatalogFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, tdxzsConfigFile),
		[]byte("旧目录|880301|2|1|0|T10\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tdxzs3ConfigFile),
		[]byte("旧目录|881001|12|1|0|X10\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, infoHarborBlockFile),
		[]byte("#GN_旧概念,1,880501,20200101,20260101,,\n0#000001\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeZipFile(t *testing.T, writer *zip.Writer, name string, content []byte) {
	t.Helper()
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
}
