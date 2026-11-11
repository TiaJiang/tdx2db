package tdx

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jing2uo/tdx2db/model"
)

func TestDecodeF10BlocksFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/f10_blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	response, err := decodeOnlineBlocks(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1: %+v", len(response.Blocks), response.Blocks)
	}
	if response.Blocks[0].BlockCode != "sh000300" ||
		response.Blocks[0].BlockType != blockTypeIndex {
		t.Fatalf("unexpected block: %+v", response.Blocks[0])
	}
	if len(response.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(response.Members))
	}
	if len(response.Warnings) != 1 || response.Warnings[0] != "sample warning" {
		t.Fatalf("unexpected warnings: %+v", response.Warnings)
	}
	if len(response.FailedBlocks) != 1 || response.FailedBlocks[0] != "sh880522" {
		t.Fatalf("unexpected failed blocks: %+v", response.FailedBlocks)
	}
}

func TestBuildF10BlockRequest(t *testing.T) {
	request := buildF10BlockRequest([]model.BlockInfo{
		{BlockType: blockTypeIndustry, BlockCode: "sh880301", BlockName: "煤炭"},
		{BlockType: blockTypeIndustry, BlockCode: "sh881001", BlockName: "煤炭"},
		{BlockType: blockTypeConcept, BlockCode: "sh880522", BlockName: "概念"},
	}, []string{"000300", "sz399006", "sh000300"})

	if len(request.Themes) != 2 {
		t.Fatalf("themes = %d, want 2: %+v", len(request.Themes), request.Themes)
	}
	if request.Themes[0].BlockCode != "sh880522" || request.Themes[1].BlockCode != "sh881001" {
		t.Fatalf("unexpected themes: %+v", request.Themes)
	}
	if len(request.Indices) != 2 {
		t.Fatalf("indices = %d, want 2: %+v", len(request.Indices), request.Indices)
	}
	if request.Indices[0].BlockCode != "sh000300" || request.Indices[1].BlockCode != "sz399006" {
		t.Fatalf("unexpected indices: %+v", request.Indices)
	}
}

func TestBuildF10BlockRequestUsesEmptyArrays(t *testing.T) {
	data, err := json.Marshal(buildF10BlockRequest(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"themes":[],"indices":[]}` {
		t.Fatalf("empty request = %s", data)
	}
}

func TestNormalizeMarketSymbol(t *testing.T) {
	tests := []struct {
		market string
		code   string
		want   string
	}{
		{market: "1", code: "600000", want: "sh600000"},
		{market: "0", code: "000001", want: "sz000001"},
		{market: "2", code: "920001", want: "bj920001"},
		{market: "", code: "880522", want: "sh880522"},
		{market: "", code: "881001", want: "sh881001"},
		{market: "", code: "000300", want: "sh000300"},
		{market: "", code: "399006", want: "sz399006"},
	}
	for _, tt := range tests {
		if got := normalizeMarketSymbol(tt.market, tt.code); got != tt.want {
			t.Errorf("normalizeMarketSymbol(%q, %q) = %q, want %q", tt.market, tt.code, got, tt.want)
		}
	}
}

func TestApplyPreviousPreservesUnavailableBlocks(t *testing.T) {
	result := &BlockFetchResult{
		Blocks: []model.BlockInfo{
			{BlockType: blockTypeIndex, BlockName: "沪深300", BlockCode: "sh000300", BlockSymbol: "sh000300", BlockLevel: 1},
		},
		Members:             []model.BlockMember{{StockSymbol: "sh600000", BlockCode: "sh000300"}},
		FailedBlocks:        []string{"sh880522"},
		PreserveAllNonIndex: true,
		Preserve880Members:  true,
	}
	result.ApplyPrevious([]model.BlockInfo{
		{BlockType: blockTypeConcept, BlockName: "旧概念", BlockCode: "sh880522", BlockSymbol: "sh880522", BlockLevel: 1},
		{BlockType: blockTypeIndustry, BlockName: "旧行业", BlockCode: "sh880301", BlockSymbol: "sh880301", BlockLevel: 1},
	}, []model.BlockMember{
		{StockSymbol: "sz000001", BlockCode: "sh880522"},
		{StockSymbol: "sz000002", BlockCode: "sh880301"},
	})

	if len(result.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3: %+v", len(result.Blocks), result.Blocks)
	}
	if len(result.Members) != 3 {
		t.Fatalf("members = %d, want 3: %+v", len(result.Members), result.Members)
	}
}

func TestApplyPreviousPreservesFailedIndex(t *testing.T) {
	result := &BlockFetchResult{FailedBlocks: []string{"sh000300"}}
	result.ApplyPrevious(
		[]model.BlockInfo{
			{BlockType: blockTypeIndex, BlockName: "沪深300", BlockCode: "sh000300", BlockSymbol: "sh000300", BlockLevel: 1},
		},
		[]model.BlockMember{{StockSymbol: "sh600000", BlockCode: "sh000300"}},
	)
	if len(result.Blocks) != 1 || len(result.Members) != 1 {
		t.Fatalf("failed index was not preserved: blocks=%+v members=%+v", result.Blocks, result.Members)
	}
}

func TestPreserveAllNonIndexDoesNotImplicitlyPreserve881(t *testing.T) {
	result := &BlockFetchResult{PreserveAllNonIndex: true}
	result.ApplyPrevious(
		[]model.BlockInfo{
			{BlockType: blockTypeIndustry, BlockName: "旧881", BlockCode: "sh881001", BlockSymbol: "sh881001", BlockLevel: 1},
		},
		[]model.BlockMember{{StockSymbol: "sh600000", BlockCode: "sh881001"}},
	)
	if len(result.Blocks) != 0 || len(result.Members) != 0 {
		t.Fatalf("881 should require Preserve881: blocks=%+v members=%+v", result.Blocks, result.Members)
	}
}

func TestEmbeddedBlockBridgeHasNoMacCalls(t *testing.T) {
	for _, forbidden := range []string{"MacClient", "get_board_list", "get_board_members", "eltdx.mac"} {
		if strings.Contains(eltdxOnlineScript, forbidden) {
			t.Fatalf("embedded block bridge contains forbidden MAC reference %q", forbidden)
		}
	}
}

func TestEmbeddedBlockBridgeIsSerial(t *testing.T) {
	for _, forbidden := range []string{"ThreadPoolExecutor", "F10_WORKERS"} {
		if strings.Contains(eltdxOnlineScript, forbidden) {
			t.Fatalf("embedded block bridge contains concurrent execution reference %q", forbidden)
		}
	}
}
