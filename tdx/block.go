package tdx

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jing2uo/tdx2db/model"
)

type eltdxBlockInfo struct {
	BlockType   string `json:"block_type"`
	BlockName   string `json:"block_name"`
	BlockSymbol string `json:"block_symbol"`
	BlockCode   string `json:"block_code"`
	ParentCode  string `json:"parent_code"`
	BlockLevel  int    `json:"block_level"`
}

type eltdxBlockMember struct {
	StockSymbol string `json:"stock_symbol"`
	BlockCode   string `json:"block_code"`
}

type eltdxBlocksResponse struct {
	Blocks  []eltdxBlockInfo   `json:"blocks"`
	Members []eltdxBlockMember `json:"members"`
}

func FetchOnlineBlocks(ctx context.Context) ([]model.BlockInfo, []model.BlockMember, error) {
	data, err := runEltdx(ctx, "blocks")
	if err != nil {
		return nil, nil, err
	}
	return decodeOnlineBlocks(ctx, data)
}

func decodeOnlineBlocks(ctx context.Context, data []byte) ([]model.BlockInfo, []model.BlockMember, error) {
	var response eltdxBlocksResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, nil, fmt.Errorf("decode eltdx blocks: %w", err)
	}

	infoByCode := make(map[string]model.BlockInfo, len(response.Blocks))
	for i, item := range response.Blocks {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		blockCode := strings.TrimSpace(item.BlockCode)
		blockName := strings.TrimSpace(item.BlockName)
		if blockCode == "" || blockName == "" {
			continue
		}
		blockSymbol := strings.TrimSpace(item.BlockSymbol)
		if blockSymbol == "" {
			blockSymbol = blockCode
		}
		infoByCode[blockCode] = model.BlockInfo{
			BlockType:   strings.TrimSpace(item.BlockType),
			BlockName:   blockName,
			BlockSymbol: blockSymbol,
			BlockCode:   blockCode,
			ParentCode:  strings.TrimSpace(item.ParentCode),
			BlockLevel:  item.BlockLevel,
		}
	}

	memberSet := make(map[string]model.BlockMember, len(response.Members))
	for i, item := range response.Members {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		stockSymbol := strings.TrimSpace(item.StockSymbol)
		blockCode := strings.TrimSpace(item.BlockCode)
		if stockSymbol == "" || blockCode == "" {
			continue
		}
		member := model.BlockMember{StockSymbol: stockSymbol, BlockCode: blockCode}
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}

	infos := make([]model.BlockInfo, 0, len(infoByCode))
	for _, v := range infoByCode {
		infos = append(infos, v)
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].BlockType == infos[j].BlockType {
			return infos[i].BlockSymbol < infos[j].BlockSymbol
		}
		return infos[i].BlockType < infos[j].BlockType
	})

	members := make([]model.BlockMember, 0, len(memberSet))
	for _, v := range memberSet {
		members = append(members, v)
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].BlockCode == members[j].BlockCode {
			return members[i].StockSymbol < members[j].StockSymbol
		}
		return members[i].BlockCode < members[j].BlockCode
	})

	return infos, members, nil
}
