package tdx

import (
	"strings"
	"testing"

	"github.com/jing2uo/tdx2db/model"
)

func TestEmbeddedBlockBridgeHasNoF10Calls(t *testing.T) {
	for _, forbidden := range []string{
		"F10Client", "fetch_blocks", "fetch_index_task", "call_with_retry",
		"index_members", "theme_market", "theme_rows", "fetch_theme_task", "200744",
	} {
		if strings.Contains(eltdxOnlineScript, forbidden) {
			t.Fatalf("embedded block bridge contains forbidden F10 reference %q", forbidden)
		}
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
		{market: "", code: "399300", want: "sz399300"},
	}
	for _, tt := range tests {
		if got := normalizeMarketSymbol(tt.market, tt.code); got != tt.want {
			t.Errorf("normalizeMarketSymbol(%q, %q) = %q, want %q", tt.market, tt.code, got, tt.want)
		}
	}
}

func TestNormalizeBlockCodeHandlesIndexMarkets(t *testing.T) {
	tests := map[string]string{
		"000300":   "sh000300",
		"399300":   "sz399300",
		"sh000300": "sh000300",
		"sz399300": "sz399300",
	}
	for code, want := range tests {
		if got := normalizeBlockCode(code); got != want {
			t.Errorf("normalizeBlockCode(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestApplyPreviousPreservesUnavailableBlocks(t *testing.T) {
	result := &BlockFetchResult{
		Blocks: []model.BlockInfo{
			{BlockType: blockTypeIndex, BlockName: "沪深300", BlockCode: "sh000300", BlockSymbol: "sh000300", BlockLevel: 1},
		},
		Members:             []model.BlockMember{{StockSymbol: "sh600000", BlockCode: "sh000300"}},
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

func TestApplyPreviousPreservesEmptyUnlistedIndices(t *testing.T) {
	result := &BlockFetchResult{PreserveUnlistedIndices: true}
	result.ApplyPrevious(
		[]model.BlockInfo{
			{BlockType: blockTypeIndex, BlockName: "中证1000", BlockCode: "sh000852", BlockSymbol: "sh000852", BlockLevel: 1},
			{BlockType: blockTypeIndex, BlockName: "中证1000", BlockCode: "sz399852", BlockSymbol: "sz399852", BlockLevel: 1},
		},
		[]model.BlockMember{
			{StockSymbol: "sh600000", BlockCode: "sh000852"},
			{StockSymbol: "sz000001", BlockCode: "sz399852"},
		},
	)
	if len(result.Blocks) != 2 || len(result.Members) != 2 {
		t.Fatalf("empty index was not preserved: blocks=%+v members=%+v", result.Blocks, result.Members)
	}
}

func TestApplyPreviousDropsOldRegionBlocksAndMembers(t *testing.T) {
	result := &BlockFetchResult{
		Blocks: []model.BlockInfo{
			{BlockType: blockTypeIndex, BlockName: "沪深300", BlockCode: "sh000300", BlockSymbol: "sh000300", BlockLevel: 1},
		},
		Members:             []model.BlockMember{{StockSymbol: "sh600000", BlockCode: "sh000300"}},
		PreserveAllNonIndex: true,
		Preserve880Members:  true,
	}
	result.ApplyPrevious([]model.BlockInfo{
		{BlockType: "region", BlockName: "黑龙江", BlockCode: "sh880201", BlockSymbol: "sh880201", BlockLevel: 1},
		{BlockType: blockTypeConcept, BlockName: "旧概念", BlockCode: "sh880522", BlockSymbol: "sh880522", BlockLevel: 1},
	}, []model.BlockMember{
		{StockSymbol: "sz000001", BlockCode: "sh880201"},
		{StockSymbol: "sz000002", BlockCode: "sh880522"},
	})

	if len(result.Blocks) != 2 || len(result.Members) != 2 {
		t.Fatalf("old region data was preserved: blocks=%+v members=%+v", result.Blocks, result.Members)
	}
	for _, info := range result.Blocks {
		if info.BlockCode == "sh880201" {
			t.Fatalf("old region block remains: %+v", info)
		}
	}
	for _, member := range result.Members {
		if member.BlockCode == "sh880201" {
			t.Fatalf("old region member remains: %+v", member)
		}
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
	for _, forbidden := range []string{"ThreadPoolExecutor", "F10_WORKERS", "asyncio"} {
		if strings.Contains(eltdxOnlineScript, forbidden) {
			t.Fatalf("embedded block bridge contains concurrent execution reference %q", forbidden)
		}
	}
}
