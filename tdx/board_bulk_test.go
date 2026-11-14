package tdx

import (
	"context"
	"crypto/md5"
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

func TestParseInfoHarborAllParsesConceptAndIndexSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	text := "" +
		"#GN_概念甲,1,880501,20200101,20260101,,\n0#000001\n" +
		"#ZS_中证500,1,,20070115,,,\n1#600000\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := parseInfoHarborAll(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000905", Name: "中证500", Class: model.ClassIndex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexErr != nil {
		t.Fatal(result.IndexErr)
	}
	if len(result.Response.Blocks) != 1 || result.Response.Blocks[0].BlockCode != "sh880501" {
		t.Fatalf("unexpected concept blocks: %+v", result.Response.Blocks)
	}
	if len(result.Indices.Response.Blocks) != 1 || result.Indices.Response.Blocks[0].BlockCode != "sh000905" {
		t.Fatalf("unexpected index blocks: %+v", result.Indices.Response.Blocks)
	}
	if len(result.Indices.Response.Members) != 1 ||
		result.Indices.Response.Members[0].StockSymbol != "sh600000" {
		t.Fatalf("unexpected index members: %+v", result.Indices.Response.Members)
	}
}

func TestParseInfoHarborIndexesMapsUniqueNamesAndExpandsAliases(t *testing.T) {
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
	if result.IndexNames != 4 || result.IndexMatched != 3 ||
		result.IndexAliases != 1 || result.IndexUnmatched != 1 {
		t.Fatalf("unexpected mapping stats: %+v", result)
	}
	blockNames := make(map[string]string)
	for _, info := range result.Response.Blocks {
		blockNames[info.BlockCode] = info.BlockName
	}
	if len(blockNames) != 3 ||
		blockNames["sh000300"] != "沪深300" ||
		blockNames["sh000001"] != "重复指数" ||
		blockNames["sz399001"] != "重复指数" {
		t.Fatalf("unexpected index blocks: %+v", result.Response.Blocks)
	}
	membersByBlock := make(map[string]map[string]bool)
	for _, member := range result.Response.Members {
		if membersByBlock[member.BlockCode] == nil {
			membersByBlock[member.BlockCode] = make(map[string]bool)
		}
		membersByBlock[member.BlockCode][member.StockSymbol] = true
	}
	if len(result.Response.Members) != 4 ||
		!membersByBlock["sh000300"]["sz000001"] ||
		!membersByBlock["sh000300"]["sh600000"] ||
		!membersByBlock["sh000001"]["sz000001"] ||
		!membersByBlock["sz399001"]["sz000001"] {
		t.Fatalf("unexpected index members: %+v", result.Response.Members)
	}
	for _, info := range result.Response.Blocks {
		if info.BlockCode == "sh000905" {
			t.Fatalf("empty index block was fabricated: %+v", info)
		}
	}
	if !strings.Contains(strings.Join(result.Response.Warnings, "\n"), "空指数 没有成员") {
		t.Fatalf("expected empty-section warning: %+v", result.Response.Warnings)
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

func TestParseInfoHarborIndexesExpandsAliasesAndSkipsEmptySections(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	names := []model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sz399300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sh000852", Name: "中证1000", Class: model.ClassIndex},
		{Symbol: "sz399852", Name: "中证1000", Class: model.ClassIndex},
	}
	var text strings.Builder
	text.WriteString("#ZS_沪深300,300,,20050408,,,\n")
	for index := 0; index < 300; index++ {
		if index > 0 {
			text.WriteByte(',')
		}
		market := "0"
		code := fmt.Sprintf("%06d", index+1)
		if index%2 == 1 {
			market = "1"
			code = fmt.Sprintf("%06d", 600000+index)
		}
		fmt.Fprintf(&text, "%s#%s", market, code)
	}
	text.WriteString("\n#ZS_中证1000,0,,20200101,,,\n")
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := parseInfoHarborIndexes(context.Background(), path, names)
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexNames != 2 || result.IndexMatched != 2 ||
		result.IndexAliases != 2 || result.IndexUnmatched != 0 {
		t.Fatalf("unexpected mapping stats: %+v", result)
	}
	if len(result.Response.Blocks) != 2 {
		t.Fatalf("blocks = %+v, want sh000300 and sz399300", result.Response.Blocks)
	}
	membersByBlock := make(map[string]map[string]bool)
	for _, member := range result.Response.Members {
		if membersByBlock[member.BlockCode] == nil {
			membersByBlock[member.BlockCode] = make(map[string]bool)
		}
		membersByBlock[member.BlockCode][member.StockSymbol] = true
	}
	if len(result.Response.Members) != 600 ||
		len(membersByBlock["sh000300"]) != 300 ||
		len(membersByBlock["sz399300"]) != 300 ||
		len(membersByBlock["sh000852"]) != 0 ||
		len(membersByBlock["sz399852"]) != 0 {
		t.Fatalf("unexpected alias member coverage: %+v", membersByBlock)
	}
	for symbol := range membersByBlock["sh000300"] {
		if !membersByBlock["sz399300"][symbol] {
			t.Fatalf("alias members differ at %s", symbol)
		}
	}
	if !strings.Contains(strings.Join(result.Response.Warnings, "\n"), "中证1000 没有成员") {
		t.Fatalf("missing empty-index warning: %+v", result.Response.Warnings)
	}
}

func TestParseInfoHarborIndexesIgnoresUnmatchedNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), infoHarborBlockFile)
	text := "" +
		"#ZS_沪深300,1,,20050408,,,\n0#000001\n" +
		"#ZS_完全未匹配,1,,20200101,,,\n0#000001\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := parseInfoHarborIndexes(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IndexMatched != 1 || result.IndexUnmatched != 1 {
		t.Fatalf("unexpected unmatched index stats: %+v", result)
	}
}

func TestBuildIndexNameMapPreservesMarketsAndDeduplicates(t *testing.T) {
	got := buildIndexNameMap([]model.SymbolName{
		{Symbol: "sh000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sz399300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "000300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "399300", Name: "沪深300", Class: model.ClassIndex},
		{Symbol: "sh600000", Name: "沪深300", Class: model.ClassStock},
	})
	if strings.Join(got["沪深300"], ",") != "sh000300,sz399300" {
		t.Fatalf("index aliases = %v", got["沪深300"])
	}
}

func TestStockSymbolFromIndexMemberHandlesMarketsAndBareCodes(t *testing.T) {
	tests := []struct {
		code string
		want string
		ok   bool
	}{
		{code: "sh600000", want: "sh600000", ok: true},
		{code: "sz000001", want: "sz000001", ok: true},
		{code: "000001", want: "sz000001", ok: true},
		{code: "600000", want: "sh600000", ok: true},
		{code: "920001", want: "bj920001", ok: true},
		{code: "399001"},
		{code: "sh000300"},
		{code: "sz399300"},
	}
	for _, tt := range tests {
		got, ok := stockSymbolFromIndexMember(tt.code)
		if got != tt.want || ok != tt.ok {
			t.Errorf("stockSymbolFromIndexMember(%q) = (%q, %v), want (%q, %v)",
				tt.code, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseSpBlockUsesNameMapAndSkipsNonStockMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), spBlockFile)
	text := "" +
		"#中证500,2,,20070115,,,\n" +
		"0000001\n" +
		"1600000\n" +
		"0000001\n" +
		"1000300\n" +
		"1880301\n" +
		"123456\n" +
		"3999999\n" +
		"#融资融券,1,,,,,\n" +
		"0000001\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	response, err := parseSpBlock(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000905", Name: "中证500", Class: model.ClassIndex},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 1 || response.Blocks[0].BlockCode != "sh000905" ||
		response.Blocks[0].BlockType != blockTypeIndex {
		t.Fatalf("unexpected blocks: %+v", response.Blocks)
	}
	members := make(map[string]bool)
	for _, member := range response.Members {
		if member.BlockCode != "sh000905" {
			t.Fatalf("unexpected block member: %+v", member)
		}
		members[member.StockSymbol] = true
	}
	if len(members) != 2 || !members["sz000001"] || !members["sh600000"] {
		t.Fatalf("unexpected members: %+v", response.Members)
	}
	warnings := strings.Join(response.Warnings, "\n")
	if !strings.Contains(warnings, "2 条非法成员行") ||
		!strings.Contains(warnings, "跳过 2 个无法推断为股票的成员") {
		t.Fatalf("unexpected warnings: %+v", response.Warnings)
	}
}

func TestParseSpBlockUsesFixedMappingAndExpandsAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), spBlockFile)
	text := "" +
		"#深证成指,1,,19950123,,,\n0000001\n" +
		"#中证1000,1,,20141017,,,\n1600000\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	response, err := parseSpBlock(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000852", Name: "中证1000", Class: model.ClassIndex},
		{Symbol: "sz399852", Name: "中证1000", Class: model.ClassIndex},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]bool)
	for _, info := range response.Blocks {
		codes[info.BlockCode] = true
	}
	for _, code := range []string{"sz399001", "sh000852", "sz399852"} {
		if !codes[code] {
			t.Fatalf("missing block %s: %+v", code, response.Blocks)
		}
	}
	if len(response.Members) != 3 {
		t.Fatalf("members = %d, want 3: %+v", len(response.Members), response.Members)
	}
}

func TestParseSpBlockSkipsSectionWithExistingMappedCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), spBlockFile)
	text := "#中证500,1,,20070115,,,\n1600000\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	response, err := parseSpBlock(context.Background(), path, []model.SymbolName{
		{Symbol: "sh000905", Name: "中证500", Class: model.ClassIndex},
		{Symbol: "sz399905", Name: "中证500", Class: model.ClassIndex},
	}, map[string]bool{"sh000905": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 0 || len(response.Members) != 0 {
		t.Fatalf("existing index section was overwritten: %+v", response)
	}
}

func TestParseSpBlockDoesNotWarnWhenHeaderOmitsDeclaredCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), spBlockFile)
	text := "#中证500\n0000001\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	response, err := parseSpBlock(context.Background(), path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Members) != 1 || len(response.Warnings) != 0 {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestParseSpBlockHonorsContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), spBlockFile)
	var text strings.Builder
	text.WriteString("#中证500,1001,,20070115,,,\n")
	for index := 1; index <= 1000; index++ {
		fmt.Fprintf(&text, "0%06d\n", index)
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = parseSpBlock(ctx, path, nil, nil)
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
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
