package tdx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Blocks       []eltdxBlockInfo   `json:"blocks"`
	Members      []eltdxBlockMember `json:"members"`
	Warnings     []string           `json:"warnings"`
	FailedBlocks []string           `json:"failed_blocks"`
}

type decodedBlockResponse struct {
	Blocks       []model.BlockInfo
	Members      []model.BlockMember
	Warnings     []string
	FailedBlocks []string
}

type BlockFetchOptions struct {
	BoardDataDir string
	TempDir      string
	IndexSymbols []string
}

type BlockFetchResult struct {
	Blocks              []model.BlockInfo
	Members             []model.BlockMember
	Warnings            []string
	FailedBlocks        []string
	PreserveAllNonIndex bool
	Preserve881         bool
	Preserve880Members  bool
}

type f10BlockTask struct {
	BlockCode string `json:"block_code"`
	BlockName string `json:"block_name,omitempty"`
	BlockType string `json:"block_type,omitempty"`
}

type f10BlockRequest struct {
	Themes  []f10BlockTask `json:"themes"`
	Indices []f10BlockTask `json:"indices"`
}

func FetchOnlineBlocks(ctx context.Context, opts BlockFetchOptions) (*BlockFetchResult, error) {
	staticData, err := loadStaticBoardData(ctx, opts.BoardDataDir)
	if err != nil {
		return nil, err
	}

	request := buildF10BlockRequest(staticData.Blocks, opts.IndexSymbols)
	if len(request.Indices) == 0 {
		staticData.Warnings = append(staticData.Warnings,
			"数据库中无可用指数标的，指数板块本次无目录可更新")
	}
	response := decodedBlockResponse{}
	if len(request.Themes) > 0 || len(request.Indices) > 0 {
		response, err = fetchF10Blocks(ctx, opts.TempDir, request)
		if err != nil {
			return nil, err
		}
	}

	result := &BlockFetchResult{
		Warnings:            append(staticData.Warnings, response.Warnings...),
		FailedBlocks:        normalizeBlockCodes(response.FailedBlocks),
		PreserveAllNonIndex: staticData.PreserveAllNonIndex,
		Preserve881:         staticData.Preserve881,
		Preserve880Members:  staticData.Preserve880Members,
	}

	infos := make(map[string]model.BlockInfo, len(staticData.Blocks)+len(response.Blocks))
	for _, info := range staticData.Blocks {
		infos[info.BlockCode] = info
	}
	for _, info := range response.Blocks {
		if info.BlockCode == "" {
			continue
		}
		infos[info.BlockCode] = info
	}
	memberSet := make(map[string]model.BlockMember, len(staticData.Members)+len(response.Members))
	for _, member := range staticData.Members {
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}
	for _, member := range response.Members {
		if member.StockSymbol == "" || member.BlockCode == "" {
			continue
		}
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}

	for _, info := range infos {
		result.Blocks = append(result.Blocks, info)
	}
	for _, member := range memberSet {
		if _, ok := infos[member.BlockCode]; ok {
			result.Members = append(result.Members, member)
		}
	}
	sortBlockInfos(result.Blocks)
	sortBlockMembers(result.Members)
	return result, nil
}

func buildF10BlockRequest(blocks []model.BlockInfo, indexSymbols []string) f10BlockRequest {
	request := f10BlockRequest{
		Themes:  make([]f10BlockTask, 0),
		Indices: make([]f10BlockTask, 0),
	}
	themeSeen := make(map[string]bool)
	for _, info := range blocks {
		if info.BlockType == blockTypeIndustry && !strings.HasPrefix(info.BlockCode, "sh881") {
			continue
		}
		if info.BlockType != blockTypeConcept &&
			info.BlockType != blockTypeStyle &&
			info.BlockType != blockTypeRegion &&
			info.BlockType != blockTypeIndustry {
			continue
		}
		if themeSeen[info.BlockCode] {
			continue
		}
		themeSeen[info.BlockCode] = true
		request.Themes = append(request.Themes, f10BlockTask{
			BlockCode: info.BlockCode,
			BlockName: info.BlockName,
			BlockType: info.BlockType,
		})
	}

	indexSeen := make(map[string]bool)
	for _, symbol := range indexSymbols {
		symbol = normalizeBlockCode(symbol)
		if symbol == "" || indexSeen[symbol] {
			continue
		}
		indexSeen[symbol] = true
		request.Indices = append(request.Indices, f10BlockTask{BlockCode: symbol})
	}
	sort.Slice(request.Themes, func(i, j int) bool {
		return request.Themes[i].BlockCode < request.Themes[j].BlockCode
	})
	sort.Slice(request.Indices, func(i, j int) bool {
		return request.Indices[i].BlockCode < request.Indices[j].BlockCode
	})
	return request
}

func fetchF10Blocks(ctx context.Context, tempDir string, request f10BlockRequest) (decodedBlockResponse, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	file, err := os.CreateTemp(tempDir, "tdx-f10-blocks-*.json")
	if err != nil {
		return decodedBlockResponse{}, fmt.Errorf("failed to create F10 block task file: %w", err)
	}
	taskPath := file.Name()
	defer os.Remove(taskPath)

	if err := json.NewEncoder(file).Encode(request); err != nil {
		file.Close()
		return decodedBlockResponse{}, fmt.Errorf("failed to write F10 block task file: %w", err)
	}
	if err := file.Close(); err != nil {
		return decodedBlockResponse{}, fmt.Errorf("failed to close F10 block task file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return decodedBlockResponse{}, err
	}
	data, err := runEltdx(ctx, "blocks", filepath.Clean(taskPath))
	if err != nil {
		return decodedBlockResponse{}, err
	}
	return decodeOnlineBlocks(ctx, data)
}

func decodeOnlineBlocks(ctx context.Context, data []byte) (decodedBlockResponse, error) {
	var raw eltdxBlocksResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return decodedBlockResponse{}, fmt.Errorf("decode eltdx blocks: %w", err)
	}
	response := decodedBlockResponse{
		Warnings:     compactStrings(raw.Warnings),
		FailedBlocks: normalizeBlockCodes(raw.FailedBlocks),
	}
	infoByCode := make(map[string]model.BlockInfo, len(raw.Blocks))
	for i, item := range raw.Blocks {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return decodedBlockResponse{}, err
			}
		}
		info := normalizeBlockInfo(item)
		if info.BlockCode != "" {
			infoByCode[info.BlockCode] = info
		}
	}
	memberSet := make(map[string]model.BlockMember, len(raw.Members))
	for i, item := range raw.Members {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return decodedBlockResponse{}, err
			}
		}
		member := normalizeBlockMember(item)
		if member.StockSymbol != "" && member.BlockCode != "" {
			memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
		}
	}
	for _, info := range infoByCode {
		response.Blocks = append(response.Blocks, info)
	}
	for _, member := range memberSet {
		response.Members = append(response.Members, member)
	}
	sortBlockInfos(response.Blocks)
	sortBlockMembers(response.Members)
	return response, nil
}

func normalizeBlockInfo(raw eltdxBlockInfo) model.BlockInfo {
	info := model.BlockInfo{
		BlockType:   strings.TrimSpace(raw.BlockType),
		BlockName:   strings.TrimSpace(raw.BlockName),
		BlockSymbol: normalizeBlockCode(raw.BlockSymbol),
		BlockCode:   normalizeBlockCode(raw.BlockCode),
		ParentCode:  normalizeBlockCode(raw.ParentCode),
		BlockLevel:  raw.BlockLevel,
	}
	if info.BlockSymbol == "" {
		info.BlockSymbol = info.BlockCode
	}
	if info.BlockLevel < 1 {
		info.BlockLevel = 1
	}
	if !isBlockType(info.BlockType) {
		return model.BlockInfo{}
	}
	if info.BlockCode == "" || info.BlockName == "" {
		return model.BlockInfo{}
	}
	return info
}

func normalizeBlockMember(raw eltdxBlockMember) model.BlockMember {
	return model.BlockMember{
		StockSymbol: normalizeSymbolCode(raw.StockSymbol),
		BlockCode:   normalizeBlockCode(raw.BlockCode),
	}
}

func isBlockType(blockType string) bool {
	switch blockType {
	case blockTypeIndustry, blockTypeConcept, blockTypeStyle, blockTypeRegion, blockTypeIndex:
		return true
	default:
		return false
	}
}

func normalizeSymbolCode(code string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	if strings.HasPrefix(code, "sh") || strings.HasPrefix(code, "sz") || strings.HasPrefix(code, "bj") {
		return code
	}
	if symbol, ok := model.SymbolFromCode(code); ok {
		return symbol
	}
	return normalizeMarketSymbol("", code)
}

func normalizeBlockCodes(codes []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		code = normalizeBlockCode(code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		result = append(result, code)
	}
	sort.Strings(result)
	return result
}

func compactStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (r *BlockFetchResult) ApplyPrevious(blocks []model.BlockInfo, members []model.BlockMember) {
	newCodes := make(map[string]bool, len(r.Blocks))
	for _, info := range r.Blocks {
		newCodes[info.BlockCode] = true
	}
	failed := make(map[string]bool, len(r.FailedBlocks))
	for _, code := range r.FailedBlocks {
		failed[code] = true
	}
	oldInfoByCode := make(map[string]model.BlockInfo, len(blocks))
	for _, info := range blocks {
		oldInfoByCode[info.BlockCode] = info
		if newCodes[info.BlockCode] || (!failed[info.BlockCode] && !r.shouldPreserveInfo(info)) {
			continue
		}
		r.Blocks = append(r.Blocks, info)
		newCodes[info.BlockCode] = true
	}

	memberSet := make(map[string]model.BlockMember, len(r.Members)+len(members))
	for _, member := range r.Members {
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}
	for _, member := range members {
		preserve := failed[member.BlockCode] ||
			r.shouldPreserveInfo(oldInfoByCode[member.BlockCode]) ||
			(r.Preserve880Members && strings.HasPrefix(member.BlockCode, "sh880"))
		if !preserve {
			continue
		}
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}

	r.Members = r.Members[:0]
	for _, member := range memberSet {
		if newCodes[member.BlockCode] {
			r.Members = append(r.Members, member)
		}
	}
	sortBlockInfos(r.Blocks)
	sortBlockMembers(r.Members)
}

func (r *BlockFetchResult) shouldPreserveInfo(info model.BlockInfo) bool {
	if info.BlockCode == "" {
		return false
	}
	if r.PreserveAllNonIndex && info.BlockType != blockTypeIndex &&
		!strings.HasPrefix(info.BlockCode, "sh881") {
		return true
	}
	return r.Preserve881 && strings.HasPrefix(info.BlockCode, "sh881")
}

func sortBlockInfos(infos []model.BlockInfo) {
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].BlockType == infos[j].BlockType {
			return infos[i].BlockCode < infos[j].BlockCode
		}
		return infos[i].BlockType < infos[j].BlockType
	})
}

func sortBlockMembers(members []model.BlockMember) {
	sort.Slice(members, func(i, j int) bool {
		if members[i].BlockCode == members[j].BlockCode {
			return members[i].StockSymbol < members[j].StockSymbol
		}
		return members[i].BlockCode < members[j].BlockCode
	})
}
