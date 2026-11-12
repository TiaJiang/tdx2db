package tdx

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestLoadStaticBoardData(t *testing.T) {
	dir := t.TempDir()
	tdxzs := "" +
		"煤炭|880301|2|1|0|T10\n" +
		"煤炭开采|880302|2|1|1|T1001\n" +
		"黑龙江|880201|3|1|0|1\n" +
		"长三角|880526|4|1|0|长三角\n" +
		"即将解禁|880897|5|1|0|即将解禁\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(tdxzs))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tdxzs.cfg"), encoded, 0644); err != nil {
		t.Fatal(err)
	}
	tdxzs3 := "" +
		"煤炭|881001|12|1|0|X10\n" +
		"煤炭开采|881002|12|1|0|X1001\n" +
		"动力煤|881003|12|1|1|X100101\n"
	if err := os.WriteFile(filepath.Join(dir, "tdxzs3.cfg"), []byte(tdxzs3), 0644); err != nil {
		t.Fatal(err)
	}
	tdxhy := "1|600000|T1001|||X100101\n0|000001|T1001|||X100101\n"
	if err := os.WriteFile(filepath.Join(dir, "tdxhy.cfg"), []byte(tdxhy), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := loadStaticBoardData(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Blocks) != 8 {
		t.Fatalf("blocks = %d, want 8: %+v", len(data.Blocks), data.Blocks)
	}
	if len(data.Members) != 10 {
		t.Fatalf("members = %d, want 10: %+v", len(data.Members), data.Members)
	}

	infoByCode := make(map[string]struct {
		blockType string
		level     int
		parent    string
	})
	for _, info := range data.Blocks {
		if info.BlockType == "tdx_research" {
			t.Fatalf("unexpected tdx_research block: %+v", info)
		}
		infoByCode[info.BlockCode] = struct {
			blockType string
			level     int
			parent    string
		}{blockType: info.BlockType, level: info.BlockLevel, parent: info.ParentCode}
	}
	if got := infoByCode["sh880302"]; got.blockType != blockTypeIndustry ||
		got.level != 2 || got.parent != "sh880301" {
		t.Fatalf("unexpected 880 child: %+v", got)
	}
	if got := infoByCode["sh881003"]; got.blockType != blockTypeIndustry ||
		got.level != 3 || got.parent != "sh881002" {
		t.Fatalf("unexpected 881 leaf: %+v", got)
	}
	for code, blockType := range map[string]string{
		"sh880201": blockTypeRegion,
		"sh880526": blockTypeConcept,
		"sh880897": blockTypeStyle,
	} {
		if got := infoByCode[code]; got.blockType != blockType {
			t.Fatalf("%s type = %q, want %q", code, got.blockType, blockType)
		}
	}
}

func TestLoadStaticBoardDataMissingDirectoryPreservesOldNonIndex(t *testing.T) {
	data, err := loadStaticBoardData(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if !data.PreserveAllNonIndex {
		t.Fatal("expected missing static directory to preserve old non-index blocks")
	}
	if !data.Preserve881 {
		t.Fatal("expected missing static directory to preserve old 881 blocks")
	}
	if len(data.Warnings) == 0 {
		t.Fatal("expected missing static directory warning")
	}
}

func TestLoadStaticBoardDataParsesRegionDBF(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tdxzs.cfg"),
		[]byte("黑龙江|880201|3|1|0|42\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tdxzs3.cfg"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tdxhy.cfg"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	writeRegionDBFFixture(t, filepath.Join(dir, "base.dbf"), []regionDBFRecord{
		{market: "1", code: "600000", region: "42"},
		{market: "0", code: "000001", region: "42"},
		{market: "1", code: "600001", region: "99"},
	})

	data, err := loadStaticBoardData(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if data.RegionFallback || data.PreserveRegion {
		t.Fatalf("unexpected region preservation state: %+v", data)
	}
	found := make(map[string]bool)
	for _, member := range data.Members {
		if member.BlockCode == "sh880201" {
			found[member.StockSymbol] = true
		}
	}
	if !found["sh600000"] || !found["sz000001"] || len(found) != 2 {
		t.Fatalf("unexpected region members: %+v", data.Members)
	}
}

func TestLoadStaticBoardDataFallsBackToF10WhenRegionDBFMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tdxzs.cfg"),
		[]byte("黑龙江|880201|3|1|0|42\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tdxzs3.cfg"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tdxhy.cfg"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	data, err := loadStaticBoardData(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !data.RegionFallback || data.PreserveRegion {
		t.Fatalf("unexpected region state: %+v", data)
	}
	request := buildF10BlockRequest(data.Blocks, data.RegionFallback)
	if len(request.Themes) != 1 || request.Themes[0].BlockCode != "sh880201" {
		t.Fatalf("unexpected region F10 request: %+v", request)
	}
}

type regionDBFRecord struct {
	market string
	code   string
	region string
}

func writeRegionDBFFixture(t *testing.T, path string, rows []regionDBFRecord) {
	t.Helper()
	const headerSize = 32 + 3*32 + 1
	const recordSize = 1 + 1 + 6 + 2
	data := make([]byte, headerSize+len(rows)*recordSize+1)
	data[0] = 0x03
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(rows)))
	binary.LittleEndian.PutUint16(data[8:10], headerSize)
	binary.LittleEndian.PutUint16(data[10:12], recordSize)
	fields := []struct {
		name string
		size int
	}{
		{name: "SC", size: 1},
		{name: "GPDM", size: 6},
		{name: "DY", size: 2},
	}
	for index, field := range fields {
		pos := 32 + index*32
		copy(data[pos:pos+11], []byte(field.name))
		data[pos+16] = byte(field.size)
	}
	data[headerSize-1] = 0x0d
	for index, row := range rows {
		start := headerSize + index*recordSize
		data[start] = ' '
		copy(data[start+1:start+2], row.market)
		copy(data[start+2:start+8], row.code)
		copy(data[start+8:start+10], fmt.Sprintf("%-2s", row.region))
	}
	data[len(data)-1] = 0x1a
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
