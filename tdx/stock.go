package tdx

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jing2uo/tdx2db/model"
)

type eltdxSymbol struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

type eltdxSymbolsResponse struct {
	Symbols []eltdxSymbol `json:"symbols"`
}

func FetchOnlineSymbolNames(ctx context.Context) ([]model.SymbolName, error) {
	data, err := runEltdx(ctx, "symbols")
	if err != nil {
		return nil, err
	}
	return decodeOnlineSymbolNames(ctx, data)
}

func decodeOnlineSymbolNames(ctx context.Context, data []byte) ([]model.SymbolName, error) {
	var response eltdxSymbolsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode eltdx symbols: %w", err)
	}

	names := make([]model.SymbolName, 0, len(response.Symbols))
	for i, item := range response.Symbols {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		symbol := strings.TrimSpace(item.Symbol)
		name := strings.TrimSpace(item.Name)
		if symbol == "" || name == "" {
			continue
		}
		class := model.ClassifyCode(symbol)
		if !isOnlineSymbolNameClass(class) {
			continue
		}
		names = append(names, model.SymbolName{
			Symbol: symbol,
			Name:   name,
			Class:  class,
		})
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Symbol < names[j].Symbol })

	return names, nil
}

func isOnlineSymbolNameClass(class string) bool {
	switch class {
	case model.ClassStock, model.ClassBStock, model.ClassETF, model.ClassCBond, model.ClassIndex:
		return true
	}
	return false
}
