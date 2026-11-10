package tdx

import (
	"context"
	"testing"

	"github.com/jing2uo/tdx2db/model"
)

func TestDecodeOnlineSymbolNames(t *testing.T) {
	data := []byte(`{
		"symbols": [
			{"symbol": "sh600000", "name": "浦发银行"},
			{"symbol": "sh900901", "name": "云赛B股"},
			{"symbol": "sh510300", "name": "沪深300ETF"},
			{"symbol": "sh110001", "name": "转债"},
			{"symbol": "sh000001", "name": "上证指数"},
			{"symbol": "sh880001", "name": "板块"},
			{"symbol": "sh999999", "name": "未知"},
			{"symbol": "sz000001", "name": " "}
		]
	}`)

	names, err := decodeOnlineSymbolNames(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 5 {
		t.Fatalf("got %d names, want 5", len(names))
	}
	want := []model.SymbolName{
		{Symbol: "sh000001", Name: "上证指数", Class: model.ClassIndex},
		{Symbol: "sh110001", Name: "转债", Class: model.ClassCBond},
		{Symbol: "sh510300", Name: "沪深300ETF", Class: model.ClassETF},
		{Symbol: "sh600000", Name: "浦发银行", Class: model.ClassStock},
		{Symbol: "sh900901", Name: "云赛B股", Class: model.ClassBStock},
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %+v, want %+v", i, names[i], want[i])
		}
	}
}

func TestDecodeOnlineBlocks(t *testing.T) {
	data := []byte(`{
		"blocks": [
			{"block_type": "concept", "block_name": "概念B", "block_symbol": "sh880002", "block_code": "sh880002", "parent_code": "", "block_level": 1},
			{"block_type": "industry", "block_name": "行业A", "block_symbol": "sh880001", "block_code": "sh880001", "parent_code": "", "block_level": 2},
			{"block_type": "concept", "block_name": "概念B", "block_symbol": "sh880002", "block_code": "sh880002", "parent_code": "", "block_level": 1},
			{"block_type": "concept", "block_name": "", "block_symbol": "", "block_code": "", "parent_code": "", "block_level": 1}
		],
		"members": [
			{"stock_symbol": "sz000002", "block_code": "sh880002"},
			{"stock_symbol": "sz000001", "block_code": "sh880002"},
			{"stock_symbol": "sz000001", "block_code": "sh880002"},
			{"stock_symbol": "", "block_code": "sh880001"}
		]
	}`)

	infos, members, err := decodeOnlineBlocks(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d block infos, want 2", len(infos))
	}
	if infos[0].BlockCode != "sh880002" || infos[0].BlockType != "concept" {
		t.Fatalf("unexpected first block: %+v", infos[0])
	}
	if infos[1].BlockCode != "sh880001" || infos[1].BlockType != "industry" || infos[1].BlockLevel != 2 {
		t.Fatalf("unexpected second block: %+v", infos[1])
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	if members[0].StockSymbol != "sz000001" || members[1].StockSymbol != "sz000002" {
		t.Fatalf("unexpected members: %+v", members)
	}
}
