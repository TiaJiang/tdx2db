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
