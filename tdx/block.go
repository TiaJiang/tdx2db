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
	BoardDataDir  string
	BoardCacheDir string
	TempDir       string
	IndexNames    []model.SymbolName
}

type BlockFetchResult struct {
	Blocks                  []model.BlockInfo
	Members                 []model.BlockMember
	Warnings                []string
	FailedBlocks            []string
	PreserveAllNonIndex     bool
	Preserve881             bool
	Preserve880Members      bool
	PreserveConceptStyle    bool
	PreserveAllIndices      bool
	PreserveUnlistedIndices bool
	PreserveRegionMembers   bool
	Stats                   boardBulkStats
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
	cacheDir, err := resolveBoardCacheDir(opts.BoardCacheDir)
	if err != nil {
		return nil, err
	}

	catalogResult, catalogErr := ensureOnlineBoardCatalog(ctx, cacheDir, opts.TempDir, opts.BoardDataDir)

	syncResponse, syncErr := syncBoardFiles(ctx, opts.TempDir, cacheDir)
	if syncErr != nil {
		syncResponse = eltdxBoardSyncResponse{}
	}
	syncedFiles := make(map[string]boardFileSync, len(syncResponse.Files))
	for _, file := range syncResponse.Files {
		syncedFiles[file.Name] = file
	}

	staticDir, tdxhySnapshot, err := prepareStaticBoardDir(
		opts.TempDir, cacheDir, opts.BoardDataDir, syncedFiles,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare static board data: %w", err)
	}
	defer os.RemoveAll(staticDir)
	staticData, err := loadStaticBoardData(ctx, staticDir)
	if err != nil {
		return nil, err
	}
	if tdxhySnapshot != nil && tdxhySnapshot.Status == "downloaded" {
		if err := commitBoardSnapshot(tdxhySnapshot.StagingPath, tdxhySnapshot.CachePath); err != nil {
			return nil, err
		}
		if tdxhySnapshot.MD5 != "" {
			if err := updateBoardCacheMetadata(
				cacheDir, tdxhySnapshot.Name, tdxhySnapshot.MD5, tdxhySnapshot.Size,
			); err != nil {
				return nil, err
			}
		}
	}

	result := &BlockFetchResult{
		Warnings:              append([]string(nil), staticData.Warnings...),
		PreserveAllNonIndex:   staticData.PreserveAllNonIndex,
		Preserve881:           staticData.Preserve881,
		Preserve880Members:    staticData.Preserve880Members,
		PreserveRegionMembers: staticData.PreserveRegion,
		Stats: boardBulkStats{
			MetaRequests:     syncResponse.MetaRequests,
			FileDownloads:    syncResponse.FileDownloads,
			ChunkRequests:    chunkRequests(syncResponse.Files),
			CatalogChecks:    catalogResult.Checks,
			CatalogDownloads: catalogResult.Downloads,
		},
	}
	result.Warnings = append(result.Warnings, catalogResult.Warnings...)
	if catalogErr != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("在线板块目录不可用，保留旧板块数据: %v", catalogErr))
	}
	if syncErr != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("tdxhy.cfg 批量同步失败，保留旧行业数据: %v", syncErr))
	}

	result.Warnings = append(result.Warnings, syncResponse.Warnings...)

	infoHarborResponse := decodedBlockResponse{}
	infoHarborFile, hasInfoHarbor := syncedFiles[infoHarborBlockFile]
	if !hasInfoHarbor {
		candidate := filepath.Join(cacheDir, infoHarborBlockFile)
		if _, statErr := os.Stat(candidate); statErr == nil {
			infoHarborFile = boardFileSync{
				Name:      infoHarborBlockFile,
				Status:    "cached",
				CachePath: candidate,
			}
			hasInfoHarbor = true
		}
	}
	indexResponse := indexBlockParseResult{}
	if hasInfoHarbor {
		parsed, loadErr := loadBoardSnapshot(ctx, cacheDir, infoHarborFile, func(ctx context.Context, path string) (infoHarborParseResult, error) {
			return parseInfoHarborAll(ctx, path, opts.IndexNames)
		})
		if loadErr != nil {
			result.PreserveConceptStyle = true
			result.PreserveAllIndices = true
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s 解析失败，保留旧概念、风格和指数数据: %v", infoHarborBlockFile, loadErr))
		} else {
			infoHarborResponse = parsed.Response
			indexResponse = parsed.Indices
			if parsed.IndexErr != nil {
				result.PreserveAllIndices = true
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("%s 指数段解析失败，保留旧指数数据: %v", infoHarborBlockFile, parsed.IndexErr))
			} else {
				result.PreserveUnlistedIndices = true
				result.PreserveAllIndices = indexResponse.PreserveAllIndices
				result.Stats.IndexNames = indexResponse.IndexNames
				result.Stats.IndexMatched = indexResponse.IndexMatched
				result.Stats.IndexAmbiguous = indexResponse.IndexAmbiguous
				result.Stats.IndexUnmatched = indexResponse.IndexUnmatched
			}
		}
	} else {
		result.PreserveConceptStyle = true
		result.PreserveAllIndices = true
		result.Warnings = append(result.Warnings, "在线板块包中缺少 infoharbor_block.dat")
	}

	request := buildF10BlockRequest(staticData.Blocks, staticData.RegionFallback)
	request.Indices = append(request.Indices, indexResponse.FallbackIndices...)
	if len(opts.IndexNames) == 0 && !result.PreserveAllIndices {
		result.Warnings = append(result.Warnings,
			"数据库中无可用指数标的，指数板块本次无目录可更新")
	}
	f10Response := decodedBlockResponse{}
	if len(request.Themes) > 0 || len(request.Indices) > 0 {
		f10Response, err = fetchF10Blocks(ctx, opts.TempDir, request)
		if err != nil {
			return nil, err
		}
	}
	result.Stats.F10FallbackBlocks = len(request.Themes) + len(request.Indices)

	result.Warnings = append(result.Warnings, infoHarborResponse.Warnings...)
	result.Warnings = append(result.Warnings, indexResponse.Response.Warnings...)
	result.Warnings = append(result.Warnings, f10Response.Warnings...)
	result.Warnings = compactStrings(result.Warnings)
	result.FailedBlocks = normalizeBlockCodes(f10Response.FailedBlocks)

	infos := make(map[string]model.BlockInfo, len(staticData.Blocks)+len(infoHarborResponse.Blocks)+len(indexResponse.Response.Blocks)+len(f10Response.Blocks))
	for _, info := range staticData.Blocks {
		infos[info.BlockCode] = info
	}
	for _, response := range []decodedBlockResponse{infoHarborResponse, indexResponse.Response, f10Response} {
		for _, info := range response.Blocks {
			if info.BlockCode == "" {
				continue
			}
			infos[info.BlockCode] = info
		}
	}
	memberSet := make(map[string]model.BlockMember, len(staticData.Members)+len(infoHarborResponse.Members)+len(indexResponse.Response.Members)+len(f10Response.Members))
	for _, member := range staticData.Members {
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}
	for _, response := range []decodedBlockResponse{infoHarborResponse, indexResponse.Response, f10Response} {
		for _, member := range response.Members {
			if member.StockSymbol == "" || member.BlockCode == "" {
				continue
			}
			memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
		}
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

func buildF10BlockRequest(blocks []model.BlockInfo, regionFallback bool) f10BlockRequest {
	request := f10BlockRequest{
		Themes:  make([]f10BlockTask, 0),
		Indices: make([]f10BlockTask, 0),
	}
	if !regionFallback {
		return request
	}
	themeSeen := make(map[string]bool)
	for _, info := range blocks {
		if info.BlockType != blockTypeRegion {
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

	sort.Slice(request.Themes, func(i, j int) bool {
		return request.Themes[i].BlockCode < request.Themes[j].BlockCode
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
		oldInfo := oldInfoByCode[member.BlockCode]
		preserve := failed[member.BlockCode] ||
			(r.PreserveConceptStyle && isConceptOrStyle(oldInfo)) ||
			(r.PreserveRegionMembers && oldInfo.BlockType == blockTypeRegion) ||
			(!newCodes[member.BlockCode] && r.shouldPreserveInfo(oldInfo)) ||
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
	if r.PreserveConceptStyle && isConceptOrStyle(info) {
		return true
	}
	if (r.PreserveAllIndices || r.PreserveUnlistedIndices) && info.BlockType == blockTypeIndex {
		return true
	}
	return r.Preserve881 && strings.HasPrefix(info.BlockCode, "sh881")
}

func isConceptOrStyle(info model.BlockInfo) bool {
	return info.BlockType == blockTypeConcept || info.BlockType == blockTypeStyle
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
