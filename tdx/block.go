package tdx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jing2uo/tdx2db/model"
)

type decodedBlockResponse struct {
	Blocks   []model.BlockInfo
	Members  []model.BlockMember
	Warnings []string
}

type BlockFetchOptions struct {
	BoardCacheDir string
	TempDir       string
	IndexNames    []model.SymbolName
}

type BlockFetchResult struct {
	Blocks                  []model.BlockInfo
	Members                 []model.BlockMember
	Warnings                []string
	PreserveAllNonIndex     bool
	Preserve881             bool
	Preserve880Members      bool
	PreserveConceptStyle    bool
	PreserveAllIndices      bool
	PreserveUnlistedIndices bool
	Stats                   boardBulkStats
}

func FetchOnlineBlocks(ctx context.Context, opts BlockFetchOptions) (*BlockFetchResult, error) {
	cacheDir, err := resolveBoardCacheDir(opts.BoardCacheDir)
	if err != nil {
		return nil, err
	}

	catalogResult, catalogErr := ensureOnlineBoardCatalog(ctx, cacheDir, opts.TempDir)

	syncResponse, syncErr := syncBoardFiles(ctx, opts.TempDir, cacheDir)
	if syncErr != nil {
		syncResponse = eltdxBoardSyncResponse{}
	}
	syncedFiles := make(map[string]boardFileSync, len(syncResponse.Files))
	for _, file := range syncResponse.Files {
		syncedFiles[file.Name] = file
	}

	staticDir, tdxhySnapshot, err := prepareStaticBoardDir(
		opts.TempDir, cacheDir, syncedFiles,
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
		Warnings:            append([]string(nil), staticData.Warnings...),
		PreserveAllNonIndex: staticData.PreserveAllNonIndex,
		Preserve881:         staticData.Preserve881,
		Preserve880Members:  staticData.Preserve880Members,
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
				result.Stats.IndexAliases = indexResponse.IndexAliases
				result.Stats.IndexUnmatched = indexResponse.IndexUnmatched
			}
		}
	} else {
		result.PreserveConceptStyle = true
		result.PreserveAllIndices = true
		result.Warnings = append(result.Warnings, "在线板块包中缺少 infoharbor_block.dat")
	}

	if len(opts.IndexNames) == 0 && !result.PreserveAllIndices {
		result.Warnings = append(result.Warnings,
			"数据库中无可用指数标的，指数板块本次无目录可更新")
	}

	result.Warnings = append(result.Warnings, infoHarborResponse.Warnings...)
	result.Warnings = append(result.Warnings, indexResponse.Response.Warnings...)
	result.Warnings = compactStrings(result.Warnings)

	infos := make(map[string]model.BlockInfo, len(staticData.Blocks)+len(infoHarborResponse.Blocks)+len(indexResponse.Response.Blocks))
	for _, info := range staticData.Blocks {
		infos[info.BlockCode] = info
	}
	for _, response := range []decodedBlockResponse{infoHarborResponse, indexResponse.Response} {
		for _, info := range response.Blocks {
			if info.BlockCode == "" {
				continue
			}
			infos[info.BlockCode] = info
		}
	}
	memberSet := make(map[string]model.BlockMember, len(staticData.Members)+len(infoHarborResponse.Members)+len(indexResponse.Response.Members))
	for _, member := range staticData.Members {
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}
	for _, response := range []decodedBlockResponse{infoHarborResponse, indexResponse.Response} {
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

func isRemovedBlockType(blockType string) bool {
	return blockType == "region"
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
	includedCodes := make(map[string]bool, len(r.Blocks)+len(blocks))
	for _, info := range r.Blocks {
		newCodes[info.BlockCode] = true
		includedCodes[info.BlockCode] = true
	}
	oldInfoByCode := make(map[string]model.BlockInfo, len(blocks))
	for _, info := range blocks {
		oldInfoByCode[info.BlockCode] = info
		if isRemovedBlockType(info.BlockType) {
			continue
		}
		if newCodes[info.BlockCode] || !r.shouldPreserveInfo(info) {
			continue
		}
		r.Blocks = append(r.Blocks, info)
		includedCodes[info.BlockCode] = true
	}

	memberSet := make(map[string]model.BlockMember, len(r.Members)+len(members))
	for _, member := range r.Members {
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}
	for _, member := range members {
		oldInfo := oldInfoByCode[member.BlockCode]
		if isRemovedBlockType(oldInfo.BlockType) {
			continue
		}
		preserve := (r.PreserveConceptStyle && isConceptOrStyle(oldInfo)) ||
			(!newCodes[member.BlockCode] && r.shouldPreserveInfo(oldInfo)) ||
			(r.Preserve880Members && strings.HasPrefix(member.BlockCode, "sh880"))
		if !preserve {
			continue
		}
		memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
	}

	r.Members = r.Members[:0]
	for _, member := range memberSet {
		if includedCodes[member.BlockCode] {
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
