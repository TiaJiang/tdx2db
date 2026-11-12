package tdx

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jing2uo/tdx2db/model"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestParseInfoHarborBlocksAndWarnsOnCountMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	text := "" +
		"#GN_概念甲,3,880501,20200101,20260101,,\n" +
		"0#000001,1#600000,0#000001\n" +
		"#FG_风格甲,1,880502,20200101,20260101,,\n" +
		"2#920001\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	response, err := parseInfoHarbor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(response.Blocks), response.Blocks)
	}
	if len(response.Members) != 3 {
		t.Fatalf("members = %d, want 3: %+v", len(response.Members), response.Members)
	}
	if response.Blocks[0].BlockType != blockTypeConcept ||
		response.Blocks[0].BlockCode != "sh880501" ||
		response.Blocks[1].BlockType != blockTypeStyle {
		t.Fatalf("unexpected blocks: %+v", response.Blocks)
	}
	if len(response.Warnings) != 1 || !strings.Contains(response.Warnings[0], "声明成员数 3") {
		t.Fatalf("unexpected warnings: %+v", response.Warnings)
	}
}

func TestParseInfoHarborIndexesMapsUniqueNamesAndFallsBackLocally(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	text := "" +
		"#GN_概念甲,1,880501,20200101,20260101,,\n" +
		"0#000001\n" +
		"#ZS_沪深300,4,,20050408,,,\n" +
		"0#000001,1#600000,0#399001\n" +
		"#ZS_重复指数,1,,20200101,,,\n" +
		"0#000001\n" +
		"#ZS_缺失指数,1,,20200101,,,\n" +
		"1#600000\n" +
		"#ZS_空指数,0,,20200101,,,\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := parseInfoHarborIndexes(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sh000001", Name: "重复指数", Class: model.ClassIndex},
		{Symbol: "sz399001", Name: "重复指数", Class: model.ClassIndex},
		{Symbol: "sh000905", Name: "空指数", Class: model.ClassIndex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexNames != 4 || result.IndexMatched != 2 ||
		result.IndexAmbiguous != 1 || result.IndexUnmatched != 1 {
		t.Fatalf("unexpected mapping stats: %+v", result)
	}
	if len(result.Response.Blocks) != 1 ||
		result.Response.Blocks[0].BlockCode != "sh000300" ||
		result.Response.Blocks[0].BlockName != "沪深300" {
		t.Fatalf("unexpected index blocks: %+v", result.Response.Blocks)
	}
	memberBySymbol := make(map[string]bool)
	for _, member := range result.Response.Members {
		memberBySymbol[member.StockSymbol] = true
	}
	if !memberBySymbol["sz000001"] || !memberBySymbol["sh600000"] || memberBySymbol["sz399001"] {
		t.Fatalf("unexpected index members: %+v", result.Response.Members)
	}
	if len(result.FallbackIndices) != 2 ||
		result.FallbackIndices[0].BlockCode != "sh000001" ||
		result.FallbackIndices[1].BlockCode != "sz399001" {
		t.Fatalf("unexpected fallback indices: %+v", result.FallbackIndices)
	}
	if len(result.Response.Warnings) < 2 {
		t.Fatalf("expected count and empty-section warnings: %+v", result.Response.Warnings)
	}
}

func TestParseInfoHarborIndexesDoesNotCapMembersAt400(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	var text strings.Builder
	text.WriteString("#ZS_大指数,401,,20200101,,,\n")
	for index := 1; index <= 401; index++ {
		if index > 1 {
			text.WriteByte(',')
		}
		fmt.Fprintf(&text, "0#%06d", index)
	}
	text.WriteByte('\n')
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := parseInfoHarborIndexes(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000999", Name: "大指数", Class: model.ClassIndex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Response.Members) != 401 {
		t.Fatalf("members = %d, want 401", len(result.Response.Members))
	}
}

func TestParseIndexBlocksMapsUniqueNamesAndFallsBackLocally(t *testing.T) {
	path := filepath.Join(t.TempDir(), indexBlockFile)
	writeBlockZSFixture(t, path, []blockZSFixtureRecord{
		{name: "沪深300", codes: []string{"000001", "399001", "600000"}},
		{name: "重复指数", codes: []string{"600000"}},
		{name: "缺失指数", codes: []string{"600000"}},
		{name: "精选指数", codes: []string{"399001"}},
	})
	names := []model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sh000001", Name: "重复指数", Class: model.ClassIndex},
		{Symbol: "sz399001", Name: "重复指数", Class: model.ClassIndex},
		{Symbol: "sh600000", Name: "浦发银行", Class: model.ClassStock},
	}

	result, err := parseIndexBlocks(context.Background(), path, names)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexMatched != 1 || result.IndexAmbiguous != 1 || result.IndexUnmatched != 1 {
		t.Fatalf("unexpected mapping stats: %+v", result)
	}
	if len(result.Response.Blocks) != 1 ||
		result.Response.Blocks[0].BlockCode != "sh000300" ||
		result.Response.Blocks[0].BlockName != "沪深300" {
		t.Fatalf("unexpected index blocks: %+v", result.Response.Blocks)
	}
	memberBySymbol := make(map[string]bool)
	for _, member := range result.Response.Members {
		memberBySymbol[member.StockSymbol] = true
	}
	if !memberBySymbol["sz000001"] || !memberBySymbol["sh600000"] || memberBySymbol["sz399001"] {
		t.Fatalf("unexpected index members: %+v", result.Response.Members)
	}
	if len(result.FallbackIndices) != 2 ||
		result.FallbackIndices[0].BlockCode != "sh000001" ||
		result.FallbackIndices[1].BlockCode != "sz399001" {
		t.Fatalf("unexpected fallback indices: %+v", result.FallbackIndices)
	}
	if len(result.Response.Warnings) == 0 {
		t.Fatal("expected mapping warnings")
	}
}

func TestParseIndexBlocksPreservesAllWhenNameMapUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), indexBlockFile)
	writeBlockZSFixture(t, path, []blockZSFixtureRecord{
		{name: "沪深300", codes: []string{"600000"}},
	})
	result, err := parseIndexBlocks(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MappingUnavailable || !result.PreserveAllIndices {
		t.Fatalf("mapping should be unavailable: %+v", result)
	}
	if len(result.FallbackIndices) != 0 {
		t.Fatalf("unexpected broad F10 fallback: %+v", result.FallbackIndices)
	}
}

func TestLoadBoardSnapshotKeepsOldCacheWhenParsingFails(t *testing.T) {
	cacheDir := t.TempDir()
	name := infoHarborBlockFile
	cachePath := filepath.Join(cacheDir, name)
	stagingPath := filepath.Join(cacheDir, name+".download-1")
	if err := os.WriteFile(cachePath, []byte("old-good-cache"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingPath, []byte("bad-new-cache"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := loadBoardSnapshot(context.Background(), cacheDir, boardFileSync{
		Name:        name,
		Status:      "downloaded",
		CachePath:   cachePath,
		StagingPath: stagingPath,
		MD5:         "bad",
		Size:        13,
	}, func(context.Context, string) (int, error) {
		return 0, fmt.Errorf("invalid snapshot")
	})
	if err == nil {
		t.Fatal("expected parse failure")
	}
	data, readErr := os.ReadFile(cachePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old-good-cache" {
		t.Fatalf("old cache was replaced: %q", data)
	}
	if _, statErr := os.Stat(stagingPath); !os.IsNotExist(statErr) {
		t.Fatalf("staging file was not removed: %v", statErr)
	}
}

func TestSyncBoardFilesSerialOrderAndMetadataCache(t *testing.T) {
	python := testPython(t)
	fakeRoot, manifest := writeFakeEltdx(t, map[string][]byte{
		industryMappingFile: []byte("1|600000|T1001|||X500102\n"),
	})
	callsPath := filepath.Join(fakeRoot, "calls.txt")
	t.Setenv("TDX2DB_PYTHON", python)
	t.Setenv("PYTHONPATH", fakeRoot)
	t.Setenv("ELTDX_FAKE_META", manifest)
	t.Setenv("ELTDX_FAKE_CONTENT_DIR", filepath.Join(fakeRoot, "content"))
	t.Setenv("ELTDX_FAKE_CALLS", callsPath)

	cacheDir := filepath.Join(t.TempDir(), "board-cache")
	response, err := syncBoardFiles(context.Background(), t.TempDir(), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	calls := readLines(t, callsPath)
	wantCalls := []string{
		"meta:" + industryMappingFile,
		"download:" + industryMappingFile,
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls = %q, want %q", calls, wantCalls)
	}
	if response.MetaRequests != 1 || response.FileDownloads != 1 {
		t.Fatalf("unexpected request stats: %+v", response)
	}
	if len(response.Files) != 1 || response.Files[0].Status != "downloaded" {
		t.Fatalf("unexpected sync files: %+v", response.Files)
	}
}

func TestSyncBoardFilesSkipsDownloadWhenMD5Unchanged(t *testing.T) {
	python := testPython(t)
	content := map[string][]byte{
		industryMappingFile: []byte("1|600000|T1001|||X500102\n"),
	}
	fakeRoot, manifest := writeFakeEltdx(t, content)
	callsPath := filepath.Join(fakeRoot, "calls.txt")
	t.Setenv("TDX2DB_PYTHON", python)
	t.Setenv("PYTHONPATH", fakeRoot)
	t.Setenv("ELTDX_FAKE_META", manifest)
	t.Setenv("ELTDX_FAKE_CONTENT_DIR", filepath.Join(fakeRoot, "content"))
	t.Setenv("ELTDX_FAKE_CALLS", callsPath)

	cacheDir := t.TempDir()
	metadata := boardCacheMetadata{Files: make(map[string]boardCacheFileMetadata)}
	for name, data := range content {
		if err := os.WriteFile(filepath.Join(cacheDir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		metadata.Files[name] = boardCacheFileMetadata{
			MD5:  testMD5(data),
			Size: int64(len(data)),
		}
	}
	writeBoardCacheMetadataFixture(t, cacheDir, metadata)

	response, err := syncBoardFiles(context.Background(), t.TempDir(), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	calls := readLines(t, callsPath)
	for _, call := range calls {
		if strings.HasPrefix(call, "download:") {
			t.Fatalf("unexpected download call: %v", calls)
		}
	}
	if len(calls) != 1 || response.FileDownloads != 0 {
		t.Fatalf("unexpected sync result: calls=%v response=%+v", calls, response)
	}
	for _, file := range response.Files {
		if file.Status != "unchanged" {
			t.Fatalf("%s status = %s, want unchanged", file.Name, file.Status)
		}
	}
}

func TestSyncBoardFilesKeepsOldCacheWhenDownloadFails(t *testing.T) {
	python := testPython(t)
	content := map[string][]byte{
		industryMappingFile: []byte("1|600000|T1001|||X500102\n"),
	}
	fakeRoot, manifest := writeFakeEltdx(t, content)
	callsPath := filepath.Join(fakeRoot, "calls.txt")
	t.Setenv("TDX2DB_PYTHON", python)
	t.Setenv("PYTHONPATH", fakeRoot)
	t.Setenv("ELTDX_FAKE_META", manifest)
	t.Setenv("ELTDX_FAKE_CONTENT_DIR", filepath.Join(fakeRoot, "content"))
	t.Setenv("ELTDX_FAKE_CALLS", callsPath)
	t.Setenv("ELTDX_FAKE_FAIL_DOWNLOAD", industryMappingFile)

	cacheDir := t.TempDir()
	oldPath := filepath.Join(cacheDir, industryMappingFile)
	if err := os.WriteFile(oldPath, []byte("old-info"), 0644); err != nil {
		t.Fatal(err)
	}

	response, err := syncBoardFiles(context.Background(), t.TempDir(), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if response.Files[0].Name != industryMappingFile || response.Files[0].Status != "cached" {
		t.Fatalf("failed download did not fall back to cache: %+v", response.Files)
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old-info" {
		t.Fatalf("old cache was replaced after failed download: %q", data)
	}
}

func TestResolveBoardCacheDirUsesEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "cache")
	t.Setenv("TDX2DB_BOARD_CACHE_DIR", want)
	got, err := resolveBoardCacheDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cache dir = %s, want %s", got, want)
	}
}

type blockZSFixtureRecord struct {
	name  string
	codes []string
}

func writeBlockZSFixture(t *testing.T, path string, records []blockZSFixtureRecord) {
	t.Helper()
	const recordSize = 2813
	data := make([]byte, 386+len(records)*recordSize)
	binary.LittleEndian.PutUint16(data[384:386], uint16(len(records)))
	for index, record := range records {
		base := 386 + index*recordSize
		encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(record.name))
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 9 {
			t.Fatalf("fixture name too long: %s", record.name)
		}
		copy(data[base:base+9], encoded)
		binary.LittleEndian.PutUint16(data[base+9:base+11], uint16(len(record.codes)))
		for codeIndex, code := range record.codes {
			start := base + 13 + codeIndex*7
			copy(data[start:start+7], []byte(code))
		}
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeEltdx(t *testing.T, content map[string][]byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	moduleDir := filepath.Join(root, "eltdx")
	contentDir := filepath.Join(root, "content")
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatal(err)
	}
	initScript := `import json
import os
from pathlib import Path
from types import SimpleNamespace


class Resources:
    def block_meta(self, filename):
        _call("meta:" + filename)
        rows = json.loads(os.environ["ELTDX_FAKE_META"])
        row = rows[filename]
        return SimpleNamespace(size=row["size"], md5=row["md5"])

    def download_file(self, filename, *, chunk_size=30000):
        _call("download:" + filename)
        if os.environ.get("ELTDX_FAKE_FAIL_DOWNLOAD") == filename:
            raise RuntimeError("simulated download failure")
        root = Path(os.environ["ELTDX_FAKE_CONTENT_DIR"])
        return (root / filename).read_bytes()


class TdxClient:
    def __init__(self, **kwargs):
        self.resources = Resources()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


class F10Client:
    def __init__(self, **kwargs):
        pass


def _call(value):
    path = Path(os.environ["ELTDX_FAKE_CALLS"])
    with path.open("a", encoding="utf-8") as handle:
        handle.write(value + "\n")
`
	if err := os.WriteFile(filepath.Join(moduleDir, "__init__.py"), []byte(initScript), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "hosts.py"), []byte("DEFAULT_HOSTS = ['fake']\n"), 0644); err != nil {
		t.Fatal(err)
	}

	manifest := make(map[string]map[string]any)
	for name, data := range content {
		if err := os.WriteFile(filepath.Join(contentDir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		manifest[name] = map[string]any{
			"size": len(data),
			"md5":  testMD5(data),
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return root, string(data)
}

func testPython(t *testing.T) string {
	t.Helper()
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python", "py"}
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("python is not available")
	return ""
}

func testMD5(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func writeBoardCacheMetadataFixture(t *testing.T, cacheDir string, metadata boardCacheMetadata) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, boardCacheMetadataName), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines
}
