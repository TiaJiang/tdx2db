package tdx

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/database/duckdb"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
	"golang.org/x/text/encoding/simplifiedchinese"
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
	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 1 || result.Downloads != 1 || headCalls.Load() != 1 || getCalls.Load() != 1 {
		t.Fatalf("unexpected first sync: %+v head=%d get=%d", result, headCalls.Load(), getCalls.Load())
	}
	if !hasBoardCatalog(cacheDir) {
		t.Fatal("board catalog was not written to cache")
	}
	if !boardCatalogFileExists(cacheDir, spBlockFile) {
		t.Fatalf("%s was not written to cache: %+v", spBlockFile, result)
	}

	metadata := readBoardCacheMetadata(cacheDir)
	metadata.CatalogCheckedAt = "2000-01-01"
	if err := writeBoardCacheMetadata(cacheDir, metadata); err != nil {
		t.Fatal(err)
	}
	result, err = ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir())
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

	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir())
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

func TestEnsureOnlineBoardCatalogReusesDownloadedGBBQ(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "gbbq.zip"), boardCatalogArchive(t), 0644); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checks != 0 || result.Downloads != 0 || !hasBoardCatalog(cacheDir) {
		t.Fatalf("shared gbbq.zip was not reused: %+v", result)
	}
}

func TestEnsureOnlineBoardCatalogBackfillsSpBlockWithoutNetwork(t *testing.T) {
	cacheDir := t.TempDir()
	writeCatalogFixture(t, cacheDir)
	today := time.Now().Format("2006-01-02")
	metadata := boardCacheMetadata{
		Files:               make(map[string]boardCacheFileMetadata),
		CatalogCheckedAt:    today,
		CatalogETag:         `"catalog-old"`,
		CatalogLastModified: "Wed, 11 Nov 2026 12:50:13 GMT",
	}
	writeBoardCacheMetadataFixture(t, cacheDir, metadata)

	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "gbbq.zip"), boardCatalogArchive(t), 0644); err != nil {
		t.Fatal(err)
	}
	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		networkCalls.Add(1)
	}))
	defer server.Close()
	oldURL := boardCatalogURL
	boardCatalogURL = server.URL
	t.Cleanup(func() { boardCatalogURL = oldURL })

	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if networkCalls.Load() != 0 || result.Checks != 0 || result.Downloads != 0 {
		t.Fatalf("network was used during spblock backfill: calls=%d result=%+v", networkCalls.Load(), result)
	}
	if !boardCatalogFileExists(cacheDir, spBlockFile) {
		t.Fatalf("%s was not backfilled: %+v", spBlockFile, result)
	}
	got := readBoardCacheMetadata(cacheDir)
	if got.CatalogCheckedAt != today || got.CatalogETag != metadata.CatalogETag ||
		got.CatalogLastModified != metadata.CatalogLastModified {
		t.Fatalf("catalog metadata changed: %+v", got)
	}
}

func TestEnsureOnlineBoardCatalogWarnsWhenCheckedSpBlockCannotBeBackfilled(t *testing.T) {
	cacheDir := t.TempDir()
	writeCatalogFixture(t, cacheDir)
	today := time.Now().Format("2006-01-02")
	metadata := boardCacheMetadata{
		Files:               make(map[string]boardCacheFileMetadata),
		CatalogCheckedAt:    today,
		CatalogETag:         `"catalog-old"`,
		CatalogLastModified: "Wed, 11 Nov 2026 12:50:13 GMT",
	}
	writeBoardCacheMetadataFixture(t, cacheDir, metadata)

	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		networkCalls.Add(1)
	}))
	defer server.Close()
	oldURL := boardCatalogURL
	boardCatalogURL = server.URL
	t.Cleanup(func() { boardCatalogURL = oldURL })

	result, err := ensureOnlineBoardCatalog(context.Background(), cacheDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if networkCalls.Load() != 0 || result.Checks != 0 || result.Downloads != 0 {
		t.Fatalf("network was used during unavailable spblock backfill: calls=%d result=%+v", networkCalls.Load(), result)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], spBlockFile) {
		t.Fatalf("missing spblock warning: %+v", result.Warnings)
	}
	if boardCatalogFileExists(cacheDir, spBlockFile) {
		t.Fatalf("%s was unexpectedly created", spBlockFile)
	}
	got := readBoardCacheMetadata(cacheDir)
	if got.CatalogCheckedAt != today || got.CatalogETag != metadata.CatalogETag ||
		got.CatalogLastModified != metadata.CatalogLastModified {
		t.Fatalf("catalog metadata changed: %+v", got)
	}
}

func TestInstallBoardCatalogIsolatesSpBlockFailures(t *testing.T) {
	tests := []struct {
		name    string
		spBlock []byte
	}{
		{name: "invalid", spBlock: []byte("#中证500,1,,20070115,,,\ninvalid\n")},
		{name: "missing", spBlock: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			writeCatalogFixture(t, cacheDir)
			oldSpBlock := []byte("old-spblock")
			if err := os.WriteFile(filepath.Join(cacheDir, spBlockFile), oldSpBlock, 0644); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), "gbbq.zip")
			if err := os.WriteFile(archivePath, boardCatalogArchiveWithSpBlock(t, tt.spBlock), 0644); err != nil {
				t.Fatal(err)
			}
			metadata := boardCacheMetadata{Files: make(map[string]boardCacheFileMetadata)}
			warnings, err := installBoardCatalog(
				context.Background(), cacheDir, archivePath, &metadata, "2026-11-13",
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], spBlockFile) {
				t.Fatalf("missing isolation warning: %+v", warnings)
			}
			data, err := os.ReadFile(filepath.Join(cacheDir, spBlockFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, oldSpBlock) {
				t.Fatalf("old %s was replaced: %q", spBlockFile, data)
			}
			if _, err := os.Stat(filepath.Join(cacheDir, spBlockFile+".download")); !os.IsNotExist(err) {
				t.Fatalf("staging file was not removed: %v", err)
			}
			catalog, err := os.ReadFile(filepath.Join(cacheDir, tdxzsConfigFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(catalog, []byte("煤炭")) {
				t.Fatalf("core catalog was not committed: %q", catalog)
			}
			if metadata.CatalogCheckedAt != "2026-11-13" {
				t.Fatalf("catalog check time = %q", metadata.CatalogCheckedAt)
			}
		})
	}
}

func TestCachedInfoHarborExpandsAliasesAndSkipsEmptyIndex(t *testing.T) {
	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tdx2db", "board-cache", infoHarborBlockFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("board cache is not available")
	}

	names := []model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sz399300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sh000852", Name: "中证1000", Class: model.ClassIndex},
		{Symbol: "sz399852", Name: "中证1000", Class: model.ClassIndex},
	}
	result, err := parseInfoHarborIndexes(context.Background(), path, names)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexMatched != 2 || result.IndexAliases != 2 {
		t.Fatalf("unexpected cached index stats: %+v", result)
	}
	if len(result.Response.Blocks) != 2 {
		t.Fatalf("blocks = %+v, want sh000300 and sz399300", result.Response.Blocks)
	}
	membersByBlock := make(map[string]int)
	for _, member := range result.Response.Members {
		membersByBlock[member.BlockCode]++
	}
	if membersByBlock["sh000300"] != 300 || membersByBlock["sz399300"] != 300 {
		t.Fatalf("cached alias member counts = %+v", membersByBlock)
	}
	for _, code := range []string{"sh000852", "sz399852"} {
		if membersByBlock[code] != 0 {
			t.Fatalf("empty index %s has %d fabricated members", code, membersByBlock[code])
		}
	}
	if !strings.Contains(strings.Join(result.Response.Warnings, "\n"), "中证1000 没有成员") {
		t.Fatalf("missing empty-index warning: %+v", result.Response.Warnings)
	}
}

func TestFetchOnlineBlocksExpandsIndexAliasesWithoutF10(t *testing.T) {
	python := testPython(t)
	tdxhy := []byte("1|600000|T1001|||\n")
	fakeRoot, _ := writeFakeEltdx(t, map[string][]byte{industryMappingFile: tdxhy})
	callsPath := filepath.Join(fakeRoot, "calls.txt")
	t.Setenv("TDX2DB_PYTHON", python)
	t.Setenv("PYTHONPATH", fakeRoot)
	t.Setenv("ELTDX_FAKE_CALLS", callsPath)
	t.Setenv("ELTDX_FAKE_META", "")
	t.Setenv("ELTDX_FAKE_CONTENT_DIR", filepath.Join(fakeRoot, "content"))

	cacheDir := t.TempDir()
	writeBoardCatalogFixture(t, cacheDir)
	metadata := boardCacheMetadata{
		Files:            make(map[string]boardCacheFileMetadata),
		CatalogCheckedAt: time.Now().Format("2006-01-02"),
	}
	sum := md5.Sum(tdxhy)
	metadata.Files[industryMappingFile] = boardCacheFileMetadata{
		MD5:  hex.EncodeToString(sum[:]),
		Size: int64(len(tdxhy)),
	}
	writeBoardCacheMetadataFixture(t, cacheDir, metadata)

	opts := BlockFetchOptions{
		BoardCacheDir: cacheDir,
		TempDir:       t.TempDir(),
		IndexNames: []model.SymbolName{
			{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
			{Symbol: "sz399300", Name: "沪深300", Class: model.ClassIndex},
			{Symbol: "sh000852", Name: "中证1000", Class: model.ClassIndex},
			{Symbol: "sz399852", Name: "中证1000", Class: model.ClassIndex},
			{Symbol: "sh000905", Name: "中证500", Class: model.ClassIndex},
		},
	}
	result, err := FetchOnlineBlocks(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.IndexMatched != 3 || result.Stats.IndexAliases != 2 ||
		result.Stats.IndexUnmatched != 1 || result.Stats.F10FallbackBlocks != 0 {
		t.Fatalf("unexpected index stats: %+v", result.Stats)
	}
	entries, err := filepath.Glob(filepath.Join(opts.TempDir, "tdx-board-static-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(entry, "base.dbf")); !os.IsNotExist(err) {
			t.Fatalf("base.dbf was copied into the working directory: %v", err)
		}
	}

	previous := []model.BlockInfo{
		{BlockType: "region", BlockName: "黑龙江", BlockCode: "sh880201", BlockSymbol: "sh880201", BlockLevel: 1},
	}
	previousMembers := []model.BlockMember{{StockSymbol: "sz000001", BlockCode: "sh880201"}}
	result.ApplyPrevious(previous, previousMembers)
	for _, info := range result.Blocks {
		if info.BlockType == "region" {
			t.Fatalf("region info remains: %+v", info)
		}
	}
	for _, member := range result.Members {
		if member.BlockCode == "sh880201" {
			t.Fatalf("region member remains: %+v", member)
		}
	}
	assertBlockCoverage(t, result.Blocks, result.Members)

	calls := readLines(t, callsPath)
	for _, call := range calls {
		if strings.HasPrefix(call, "f10-") {
			t.Fatalf("unexpected F10 call: %v", calls)
		}
	}
	for _, forbidden := range []string{"F10Client", "fetch_blocks", "index_members"} {
		if strings.Contains(eltdxOnlineScript, forbidden) {
			t.Fatalf("embedded bridge still contains %q", forbidden)
		}
	}
	assertBlockSnapshotReplacesRegions(t, result.Blocks, result.Members)
}

func TestFetchOnlineBlocksSupplementsEmptyIndexFromSpBlock(t *testing.T) {
	python := testPython(t)
	tdxhy := []byte("1|600000|T1001|||\n")
	fakeRoot, manifest := writeFakeEltdx(t, map[string][]byte{industryMappingFile: tdxhy})
	t.Setenv("TDX2DB_PYTHON", python)
	t.Setenv("PYTHONPATH", fakeRoot)
	t.Setenv("ELTDX_FAKE_META", manifest)
	t.Setenv("ELTDX_FAKE_CONTENT_DIR", filepath.Join(fakeRoot, "content"))
	t.Setenv("ELTDX_FAKE_CALLS", filepath.Join(fakeRoot, "calls.txt"))

	cacheDir := t.TempDir()
	writeBoardCatalogFixture(t, cacheDir)
	spBlock := []byte("" +
		"#中证500,1,,20070115,,,\n1600000\n" +
		"#中证1000,2,,20141017,,,\n0000001\n1600000\n")
	encodedSpBlock, err := simplifiedchinese.GB18030.NewEncoder().Bytes(spBlock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, spBlockFile), encodedSpBlock, 0644); err != nil {
		t.Fatal(err)
	}
	metadata := boardCacheMetadata{
		Files:            make(map[string]boardCacheFileMetadata),
		CatalogCheckedAt: time.Now().Format("2006-01-02"),
	}
	sum := md5.Sum(tdxhy)
	metadata.Files[industryMappingFile] = boardCacheFileMetadata{
		MD5:  hex.EncodeToString(sum[:]),
		Size: int64(len(tdxhy)),
	}
	writeBoardCacheMetadataFixture(t, cacheDir, metadata)

	result, err := FetchOnlineBlocks(context.Background(), BlockFetchOptions{
		BoardCacheDir: cacheDir,
		TempDir:       t.TempDir(),
		IndexNames: []model.SymbolName{
			{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
			{Symbol: "sz399300", Name: "沪深300", Class: model.ClassIndex},
			{Symbol: "sh000852", Name: "中证1000", Class: model.ClassIndex},
			{Symbol: "sz399852", Name: "中证1000", Class: model.ClassIndex},
			{Symbol: "sh000905", Name: "中证500", Class: model.ClassIndex},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Stats.IndexMatched != 3 || result.Stats.IndexAliases != 2 ||
		result.Stats.IndexUnmatched != 1 {
		t.Fatalf("unexpected index stats: %+v", result.Stats)
	}
	membersByBlock := make(map[string]map[string]bool)
	for _, member := range result.Members {
		if membersByBlock[member.BlockCode] == nil {
			membersByBlock[member.BlockCode] = make(map[string]bool)
		}
		membersByBlock[member.BlockCode][member.StockSymbol] = true
	}
	for _, code := range []string{"sh000852", "sz399852"} {
		if len(membersByBlock[code]) != 2 ||
			!membersByBlock[code]["sz000001"] || !membersByBlock[code]["sh600000"] {
			t.Fatalf("%s members = %+v", code, membersByBlock[code])
		}
	}
	if len(membersByBlock["sh000905"]) != 1 || !membersByBlock["sh000905"]["sz000001"] {
		t.Fatalf("existing sh000905 section was overwritten: %+v", membersByBlock["sh000905"])
	}
}

func assertBlockSnapshotReplacesRegions(t *testing.T, blocks []model.BlockInfo, members []model.BlockMember) {
	t.Helper()
	driver, err := duckdb.NewDuckDBDriver(&url.URL{
		Scheme: "duckdb",
		Path:   filepath.Join(t.TempDir(), "blocks.duckdb"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	oldInfo, oldMembers := writeBoardSnapshotCSVs(t, append(append([]model.BlockInfo(nil), blocks...), model.BlockInfo{
		BlockType: "region", BlockName: "黑龙江", BlockCode: "sh880201", BlockSymbol: "sh880201", BlockLevel: 1,
	}), append(append([]model.BlockMember(nil), members...), model.BlockMember{
		StockSymbol: "sz000001", BlockCode: "sh880201",
	}))
	if err := driver.ImportBlockSnapshot(oldInfo, oldMembers); err != nil {
		t.Fatal(err)
	}
	newInfo, newMembers := writeBoardSnapshotCSVs(t, blocks, members)
	if err := driver.ImportBlockSnapshot(newInfo, newMembers); err != nil {
		t.Fatal(err)
	}

	assertCount := func(query string, want int64) {
		t.Helper()
		var counts []int64
		if err := driver.QueryRows(context.Background(), query, &counts); err != nil {
			t.Fatal(err)
		}
		if len(counts) != 1 || counts[0] != want {
			t.Fatalf("query %q = %v, want %d", query, counts, want)
		}
	}
	infoTable := model.TableBlockInfo.TableName
	memberTable := model.TableBlockMember.TableName
	assertCount("SELECT COUNT(*) FROM "+infoTable+" WHERE block_type = 'region'", 0)
	assertCount("SELECT COUNT(*) FROM "+memberTable+" WHERE block_code = 'sh880201'", 0)
	assertCount("SELECT COUNT(*) FROM (SELECT block_code FROM "+infoTable+" GROUP BY block_code HAVING COUNT(*) > 1)", 0)
	assertCount("SELECT COUNT(*) FROM (SELECT stock_symbol, block_code FROM "+memberTable+" GROUP BY stock_symbol, block_code HAVING COUNT(*) > 1)", 0)
	assertCount("SELECT COUNT(*) FROM "+memberTable+" m LEFT JOIN "+infoTable+" i ON i.block_code = m.block_code WHERE i.block_code IS NULL", 0)
	assertCount("SELECT COUNT(*) FROM "+memberTable+" WHERE block_code = 'sh000300'", 1)
	assertCount("SELECT COUNT(*) FROM "+memberTable+" WHERE block_code = 'sz399300'", 1)
	assertCount("SELECT COUNT(*) FROM "+infoTable+" WHERE block_code IN ('sh000852', 'sz399852')", 0)
}

func writeBoardSnapshotCSVs(t *testing.T, infos []model.BlockInfo, members []model.BlockMember) (string, string) {
	t.Helper()
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "blocks-info.csv")
	infoWriter, err := utils.NewCSVWriter[model.BlockInfo](infoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := infoWriter.Write(infos); err != nil {
		infoWriter.Close()
		t.Fatal(err)
	}
	if err := infoWriter.Close(); err != nil {
		t.Fatal(err)
	}
	memberPath := filepath.Join(dir, "blocks-member.csv")
	memberWriter, err := utils.NewCSVWriter[model.BlockMember](memberPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := memberWriter.Write(members); err != nil {
		memberWriter.Close()
		t.Fatal(err)
	}
	if err := memberWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return infoPath, memberPath
}

func assertBlockCoverage(t *testing.T, blocks []model.BlockInfo, members []model.BlockMember) {
	t.Helper()
	infoByCode := make(map[string]model.BlockInfo, len(blocks))
	for _, info := range blocks {
		if info.BlockType == "region" {
			t.Fatalf("region block generated: %+v", info)
		}
		infoByCode[info.BlockCode] = info
	}
	for code, wantType := range map[string]string{
		"sh880301": "industry",
		"sh881001": "industry",
		"sh880501": "concept",
		"sh880502": "style",
		"sh000300": "index",
		"sz399300": "index",
		"sh000905": "index",
	} {
		if infoByCode[code].BlockType != wantType {
			t.Fatalf("%s type = %q, want %q", code, infoByCode[code].BlockType, wantType)
		}
	}
	for _, code := range []string{"sh000852", "sz399852"} {
		if _, ok := infoByCode[code]; ok {
			t.Fatalf("empty index block was fabricated: %s", code)
		}
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		key := member.StockSymbol + "\x00" + member.BlockCode
		if seen[key] {
			t.Fatalf("duplicate member: %+v", member)
		}
		seen[key] = true
		if _, ok := infoByCode[member.BlockCode]; !ok {
			t.Fatalf("orphan member: %+v", member)
		}
	}
}

func writeBoardCatalogFixture(t *testing.T, dir string) {
	t.Helper()
	text := "" +
		"#GN_概念甲,1,880501,20200101,20260101,,\n0#000001\n" +
		"#FG_风格甲,1,880502,20200101,20260101,,\n1#600000\n" +
		"#ZS_沪深300,1,,20050408,,,\n0#000001\n" +
		"#ZS_中证1000,0,,20200101,,,\n" +
		"#ZS_中证500,1,,20200101,,,\n0#000001\n" +
		"#ZS_未匹配指数,1,,20200101,,,\n0#000001\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, infoHarborBlockFile), encoded, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tdxzsConfigFile),
		[]byte("黑龙江|880201|3|1|0|42\n煤炭|880301|2|1|0|T1001\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tdxzs3ConfigFile),
		[]byte("煤炭|881001|12|1|0|X10\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, industryMappingFile),
		[]byte("1|600000|T1001|||\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.dbf"),
		[]byte("must not be read"), 0644); err != nil {
		t.Fatal(err)
	}
}

func boardCatalogArchive(t *testing.T) []byte {
	return boardCatalogArchiveWithSpBlock(t,
		[]byte("#中证500,1,,20070115,,,\n0000001\n"))
}

func boardCatalogArchiveWithSpBlock(t *testing.T, spBlock []byte) []byte {
	t.Helper()
	var inner bytes.Buffer
	innerWriter := zip.NewWriter(&inner)
	writeZipFile(t, innerWriter, tdxzsConfigFile, []byte("煤炭|880301|2|1|0|T10\n"))
	writeZipFile(t, innerWriter, tdxzs3ConfigFile, []byte("煤炭|881001|12|1|0|X10\n"))
	if spBlock != nil {
		encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes(spBlock)
		if err != nil {
			t.Fatal(err)
		}
		writeZipFile(t, innerWriter, spBlockFile, encoded)
	}
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
