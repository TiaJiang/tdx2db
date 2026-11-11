package tdx

import (
	"context"
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
