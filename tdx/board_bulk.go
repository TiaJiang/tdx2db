package tdx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jing2uo/tdx2db/model"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	boardCacheMetadataName = ".tdx2db-board-cache.json"
	infoHarborBlockFile    = "infoharbor_block.dat"
	industryMappingFile    = "tdxhy.cfg"
)

var (
	infoHarborMemberPattern = regexp.MustCompile(`([012])#(\d{6})`)
	spBlockMemberPattern    = regexp.MustCompile(`^([012])(\d{6})$`)
	spBlockIndexCodes       = map[string]string{
		"深证成指":   "399001",
		"中证A500": "000510",
		"中证500":  "000905",
		"中证1000": "000852",
		"中证2000": "932000",
		"国证2000": "399303",
	}
)

type boardSyncTask struct {
	CacheDir string   `json:"cache_dir"`
	Files    []string `json:"files"`
}

type boardFileSync struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	CachePath   string `json:"cache_path"`
	StagingPath string `json:"staging_path"`
	MD5         string `json:"md5"`
	Size        int64  `json:"size"`
	Error       string `json:"error"`
}

type eltdxBoardSyncResponse struct {
	Files         []boardFileSync `json:"files"`
	Warnings      []string        `json:"warnings"`
	MetaRequests  int             `json:"meta_requests"`
	FileDownloads int             `json:"file_downloads"`
	DownloadBytes int64           `json:"download_bytes"`
}

type boardBulkStats struct {
	MetaRequests      int
	FileDownloads     int
	ChunkRequests     int
	CatalogChecks     int
	CatalogDownloads  int
	IndexNames        int
	IndexMatched      int
	IndexAliases      int
	IndexUnmatched    int
	F10FallbackBlocks int
}

type indexBlockParseResult struct {
	Response           decodedBlockResponse
	MappingUnavailable bool
	IndexNames         int
	IndexMatched       int
	IndexAliases       int
	IndexUnmatched     int
	PreserveAllIndices bool
}

type infoHarborParseResult struct {
	Response decodedBlockResponse
	Indices  indexBlockParseResult
	IndexErr error
}

type boardCacheMetadata struct {
	Files               map[string]boardCacheFileMetadata `json:"files"`
	CatalogCheckedAt    string                            `json:"catalog_checked_at,omitempty"`
	CatalogETag         string                            `json:"catalog_etag,omitempty"`
	CatalogLastModified string                            `json:"catalog_last_modified,omitempty"`
}

type boardCacheFileMetadata struct {
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}

func resolveBoardCacheDir(configured string) (string, error) {
	if dir := strings.TrimSpace(configured); dir != "" {
		return filepath.Clean(dir), nil
	}
	if dir := strings.TrimSpace(os.Getenv("TDX2DB_BOARD_CACHE_DIR")); dir != "" {
		return filepath.Clean(dir), nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve user cache dir: %w", err)
	}
	return filepath.Join(root, "tdx2db", "board-cache"), nil
}

func syncBoardFiles(ctx context.Context, tempDir, cacheDir string) (eltdxBoardSyncResponse, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	file, err := os.CreateTemp(tempDir, "tdx-board-sync-*.json")
	if err != nil {
		return eltdxBoardSyncResponse{}, fmt.Errorf("failed to create board sync task file: %w", err)
	}
	taskPath := file.Name()
	defer os.Remove(taskPath)

	task := boardSyncTask{
		CacheDir: cacheDir,
		Files:    []string{industryMappingFile},
	}
	if err := json.NewEncoder(file).Encode(task); err != nil {
		file.Close()
		return eltdxBoardSyncResponse{}, fmt.Errorf("failed to write board sync task file: %w", err)
	}
	if err := file.Close(); err != nil {
		return eltdxBoardSyncResponse{}, fmt.Errorf("failed to close board sync task file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return eltdxBoardSyncResponse{}, err
	}
	data, err := runEltdx(ctx, "sync-blocks", filepath.Clean(taskPath))
	if err != nil {
		return eltdxBoardSyncResponse{}, err
	}
	var response eltdxBoardSyncResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return eltdxBoardSyncResponse{}, fmt.Errorf("decode eltdx board sync: %w", err)
	}
	response.Warnings = compactStrings(response.Warnings)
	return response, nil
}

func loadBoardSnapshot[T any](
	ctx context.Context,
	cacheDir string,
	file boardFileSync,
	parse func(context.Context, string) (T, error),
) (T, error) {
	var zero T
	path := file.CachePath
	staged := file.Status == "downloaded"
	if staged {
		path = file.StagingPath
	}
	if path == "" {
		return zero, fmt.Errorf("%s: cache path is empty", file.Name)
	}

	parsed, err := parse(ctx, path)
	if err != nil {
		if staged {
			_ = os.Remove(path)
		}
		return zero, err
	}
	if staged {
		if err := commitBoardSnapshot(path, file.CachePath); err != nil {
			return zero, err
		}
	}
	if file.MD5 != "" {
		if err := updateBoardCacheMetadata(cacheDir, file.Name, file.MD5, file.Size); err != nil {
			return zero, err
		}
	}
	return parsed, nil
}

func commitBoardSnapshot(stagedPath, cachePath string) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return fmt.Errorf("failed to create board cache dir: %w", err)
	}
	backupPath := cachePath + ".previous"
	_ = os.Remove(backupPath)
	if _, err := os.Stat(cachePath); err == nil {
		if err := os.Rename(cachePath, backupPath); err != nil {
			return fmt.Errorf("failed to back up board cache %s: %w", cachePath, err)
		}
	}
	if err := os.Rename(stagedPath, cachePath); err != nil {
		if _, backupErr := os.Stat(backupPath); backupErr == nil {
			_ = os.Rename(backupPath, cachePath)
		}
		return fmt.Errorf("failed to replace board cache %s: %w", cachePath, err)
	}
	_ = os.Remove(backupPath)
	return nil
}

func updateBoardCacheMetadata(cacheDir, name, checksum string, size int64) error {
	metadata := readBoardCacheMetadata(cacheDir)
	if metadata.Files == nil {
		metadata.Files = make(map[string]boardCacheFileMetadata)
	}
	metadata.Files[name] = boardCacheFileMetadata{MD5: checksum, Size: size}
	return writeBoardCacheMetadata(cacheDir, metadata)
}

func readBoardCacheMetadata(cacheDir string) boardCacheMetadata {
	metadata := boardCacheMetadata{Files: make(map[string]boardCacheFileMetadata)}
	data, err := os.ReadFile(filepath.Join(cacheDir, boardCacheMetadataName))
	if err != nil {
		return metadata
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Files == nil {
		metadata.Files = make(map[string]boardCacheFileMetadata)
	}
	return metadata
}

func writeBoardCacheMetadata(cacheDir string, metadata boardCacheMetadata) error {
	if metadata.Files == nil {
		metadata.Files = make(map[string]boardCacheFileMetadata)
	}
	path := filepath.Join(cacheDir, boardCacheMetadataName)
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode board cache metadata: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(cacheDir, ".tdx2db-board-cache-*.json")
	if err != nil {
		return fmt.Errorf("failed to create board cache metadata: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("failed to write board cache metadata: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("failed to close board cache metadata: %w", err)
	}
	if err := commitBoardSnapshot(tempPath, path); err != nil {
		return fmt.Errorf("failed to replace board cache metadata: %w", err)
	}
	return nil
}

func readBoardFileGB18030(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode %s as GB18030: %w", path, err)
	}
	return decoded, nil
}

func parseInfoHarbor(ctx context.Context, path string) (decodedBlockResponse, error) {
	decoded, err := readBoardFileGB18030(path)
	if err != nil {
		return decodedBlockResponse{}, err
	}
	return parseInfoHarborContent(ctx, path, decoded)
}

func parseInfoHarborContent(ctx context.Context, path string, decoded []byte) (decodedBlockResponse, error) {
	type currentBlock struct {
		info     model.BlockInfo
		declared int
		members  map[string]model.BlockMember
	}
	infos := make(map[string]model.BlockInfo)
	members := make(map[string]model.BlockMember)
	var current *currentBlock
	var warnings []string

	flush := func() {
		if current == nil {
			return
		}
		if current.declared != len(current.members) {
			warnings = append(warnings, fmt.Sprintf(
				"%s 声明成员数 %d，实际去重成员数 %d",
				current.info.BlockCode, current.declared, len(current.members),
			))
		}
		infos[current.info.BlockCode] = current.info
		for key, member := range current.members {
			members[key] = member
		}
	}

	lines := strings.Split(string(decoded), "\n")
	for i, line := range lines {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return decodedBlockResponse{}, err
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
			current = nil
			fields := strings.Split(line, ",")
			if len(fields) < 3 {
				continue
			}
			marker := strings.TrimPrefix(fields[0], "#")
			blockType := ""
			name := ""
			switch {
			case strings.HasPrefix(marker, "GN_"):
				blockType = blockTypeConcept
				name = strings.TrimSpace(strings.TrimPrefix(marker, "GN_"))
			case strings.HasPrefix(marker, "FG_"):
				blockType = blockTypeStyle
				name = strings.TrimSpace(strings.TrimPrefix(marker, "FG_"))
			default:
				continue
			}
			declared, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
			blockCode := normalizeBulkBlockCode(fields[2], "")
			if !isSixDigitCode(strings.TrimSpace(fields[2])) || blockCode == "" || name == "" {
				warnings = append(warnings, fmt.Sprintf(
					"%s 存在无法解析的板块头: %s", infoHarborBlockFile, line,
				))
				continue
			}
			current = &currentBlock{
				info: model.BlockInfo{
					BlockType:   blockType,
					BlockName:   name,
					BlockSymbol: blockCode,
					BlockCode:   blockCode,
					BlockLevel:  1,
				},
				declared: declared,
				members:  make(map[string]model.BlockMember),
			}
			continue
		}
		if current == nil {
			continue
		}
		for _, match := range infoHarborMemberPattern.FindAllStringSubmatch(line, -1) {
			stockSymbol := marketSymbolFromID(match[1], match[2])
			if stockSymbol == "" {
				continue
			}
			member := model.BlockMember{
				StockSymbol: stockSymbol,
				BlockCode:   current.info.BlockCode,
			}
			current.members[member.StockSymbol+"\x00"+member.BlockCode] = member
		}
	}
	flush()

	if len(infos) == 0 {
		return decodedBlockResponse{}, fmt.Errorf("%s contains no concept/style blocks", path)
	}
	return decodedBlockResponse{
		Blocks:   mapValuesSorted(infos),
		Members:  mapMembersSorted(members),
		Warnings: compactStrings(warnings),
	}, nil
}

func parseInfoHarborAll(ctx context.Context, path string, names []model.SymbolName) (infoHarborParseResult, error) {
	decoded, err := readBoardFileGB18030(path)
	if err != nil {
		return infoHarborParseResult{}, err
	}
	response, err := parseInfoHarborContent(ctx, path, decoded)
	if err != nil {
		return infoHarborParseResult{}, err
	}
	indices, indexErr := parseInfoHarborIndexesContent(ctx, path, decoded, names)
	return infoHarborParseResult{
		Response: response,
		Indices:  indices,
		IndexErr: indexErr,
	}, nil
}

func parseInfoHarborIndexes(ctx context.Context, path string, names []model.SymbolName) (indexBlockParseResult, error) {
	decoded, err := readBoardFileGB18030(path)
	if err != nil {
		return indexBlockParseResult{}, err
	}
	return parseInfoHarborIndexesContent(ctx, path, decoded, names)
}

func parseInfoHarborIndexesContent(
	ctx context.Context,
	path string,
	decoded []byte,
	names []model.SymbolName,
) (indexBlockParseResult, error) {
	type currentIndex struct {
		name     string
		declared int
		members  map[string]bool
		skipped  int
	}
	nameMap := buildIndexNameMap(names)
	result := indexBlockParseResult{}
	infos := make(map[string]model.BlockInfo)
	members := make(map[string]model.BlockMember)
	var warnings []string
	var current *currentIndex

	flush := func() {
		if current == nil {
			return
		}
		if current.name == "精选指数" {
			warnings = append(warnings, "infoharbor_block.dat: 跳过精选指数板块")
			return
		}
		if current.declared != len(current.members) {
			warnings = append(warnings, fmt.Sprintf(
				"infoharbor_block.dat: %s 声明成员数 %d，实际去重成员数 %d",
				current.name, current.declared, len(current.members),
			))
		}
		candidates := nameMap[current.name]
		if len(candidates) == 0 {
			result.IndexUnmatched++
			warnings = append(warnings, fmt.Sprintf(
				"infoharbor_block.dat: %s 未匹配 raw_symbol_name 指数代码", current.name,
			))
			return
		}
		result.IndexMatched++
		if len(candidates) > 1 {
			result.IndexAliases++
			warnings = append(warnings, fmt.Sprintf(
				"infoharbor_block.dat: %s 匹配到多个指数代码，按别名展开: %s",
				current.name, strings.Join(candidates, ","),
			))
		}

		if len(current.members) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"infoharbor_block.dat: %s 没有成员，保留旧指数数据（如有）", current.name,
			))
			return
		}
		for _, blockCode := range candidates {
			infos[blockCode] = model.BlockInfo{
				BlockType:   blockTypeIndex,
				BlockName:   current.name,
				BlockSymbol: blockCode,
				BlockCode:   blockCode,
				BlockLevel:  1,
			}
			for symbol := range current.members {
				members[symbol+"\x00"+blockCode] = model.BlockMember{
					StockSymbol: symbol,
					BlockCode:   blockCode,
				}
			}
		}
		if current.skipped > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"infoharbor_block.dat: %s 跳过 %d 个无法推断为股票的成员",
				current.name, current.skipped,
			))
		}
	}

	lines := strings.Split(string(decoded), "\n")
	for i, line := range lines {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return indexBlockParseResult{}, err
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
			current = nil
			fields := strings.Split(line, ",")
			if len(fields) < 2 {
				continue
			}
			marker := strings.TrimPrefix(fields[0], "#")
			if !strings.HasPrefix(marker, "ZS_") {
				continue
			}
			name := strings.TrimSpace(strings.TrimPrefix(marker, "ZS_"))
			if name == "" {
				continue
			}
			declared, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
			current = &currentIndex{
				name:     name,
				declared: declared,
				members:  make(map[string]bool),
			}
			result.IndexNames++
			continue
		}
		if current == nil {
			continue
		}
		for _, match := range infoHarborMemberPattern.FindAllStringSubmatch(line, -1) {
			symbol := marketSymbolFromID(match[1], match[2])
			stockSymbol, ok := stockSymbolFromIndexMember(symbol)
			if !ok {
				current.skipped++
				continue
			}
			current.members[stockSymbol] = true
		}
	}
	flush()

	if result.IndexNames == 0 {
		result.MappingUnavailable = true
		result.PreserveAllIndices = true
		warnings = append(warnings, "infoharbor_block.dat 中没有 #ZS_ 指数段，保留旧指数数据")
	} else if len(nameMap) == 0 {
		result.MappingUnavailable = true
		result.PreserveAllIndices = true
		warnings = append(warnings, "raw_symbol_name 中没有指数名称，保留旧指数数据")
	} else if result.IndexMatched == 0 {
		result.MappingUnavailable = true
		result.PreserveAllIndices = true
		warnings = append(warnings, "infoharbor_block.dat 指数名称映射整体不可用，保留旧指数数据")
	}

	result.Response = decodedBlockResponse{
		Blocks:   mapValuesSorted(infos),
		Members:  mapMembersSorted(members),
		Warnings: compactStrings(warnings),
	}
	return result, nil
}

func parseSpBlock(
	ctx context.Context,
	path string,
	names []model.SymbolName,
	existingCodes map[string]bool,
) (decodedBlockResponse, error) {
	decoded, err := readBoardFileGB18030(path)
	if err != nil {
		return decodedBlockResponse{}, err
	}

	type currentIndex struct {
		name        string
		declared    int
		hasDeclared bool
		blockCodes  []string
		members     map[string]bool
		invalid     int
		skipped     int
	}
	nameMap := buildIndexNameMap(names)
	infos := make(map[string]model.BlockInfo)
	members := make(map[string]model.BlockMember)
	var warnings []string
	var current *currentIndex

	flush := func() {
		if current == nil {
			return
		}
		for _, blockCode := range current.blockCodes {
			if existingCodes[blockCode] {
				return
			}
		}
		if current.hasDeclared && current.declared != len(current.members) {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %s 声明成员数 %d，实际去重成员数 %d",
				spBlockFile, current.name, current.declared, len(current.members),
			))
		}
		if current.invalid > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %s 存在 %d 条非法成员行", spBlockFile, current.name, current.invalid,
			))
		}
		if current.skipped > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %s 跳过 %d 个无法推断为股票的成员",
				spBlockFile, current.name, current.skipped,
			))
		}
		if len(current.members) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %s 没有合法成员，保留旧指数数据（如有）", spBlockFile, current.name,
			))
			return
		}
		for _, blockCode := range current.blockCodes {
			infos[blockCode] = model.BlockInfo{
				BlockType:   blockTypeIndex,
				BlockName:   current.name,
				BlockSymbol: blockCode,
				BlockCode:   blockCode,
				BlockLevel:  1,
			}
			for symbol := range current.members {
				members[symbol+"\x00"+blockCode] = model.BlockMember{
					StockSymbol: symbol,
					BlockCode:   blockCode,
				}
			}
		}
	}

	lines := strings.Split(string(decoded), "\n")
	for i, line := range lines {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return decodedBlockResponse{}, err
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
			current = nil
			fields := strings.Split(line, ",")
			name := strings.TrimSpace(strings.TrimPrefix(fields[0], "#"))
			fixedCode, target := spBlockIndexCodes[name]
			if !target {
				continue
			}
			blockCodes := append([]string(nil), nameMap[name]...)
			if len(blockCodes) == 0 {
				if blockCode := normalizeBulkBlockCode(fixedCode, ""); blockCode != "" {
					blockCodes = []string{blockCode}
				}
			}
			if len(blockCodes) == 0 {
				warnings = append(warnings, fmt.Sprintf(
					"%s: %s 无法确定指数代码", spBlockFile, name,
				))
				continue
			}
			declared := 0
			hasDeclared := false
			if len(fields) > 1 && strings.TrimSpace(fields[1]) != "" {
				declared, _ = strconv.Atoi(strings.TrimSpace(fields[1]))
				hasDeclared = true
			}
			current = &currentIndex{
				name:        name,
				declared:    declared,
				hasDeclared: hasDeclared,
				blockCodes:  blockCodes,
				members:     make(map[string]bool),
			}
			continue
		}
		if current == nil {
			continue
		}
		match := spBlockMemberPattern.FindStringSubmatch(line)
		if len(match) == 0 {
			current.invalid++
			continue
		}
		symbol := marketSymbolFromID(match[1], match[2])
		stockSymbol, ok := stockSymbolFromIndexMember(symbol)
		if !ok {
			current.skipped++
			continue
		}
		current.members[stockSymbol] = true
	}
	flush()

	return decodedBlockResponse{
		Blocks:   mapValuesSorted(infos),
		Members:  mapMembersSorted(members),
		Warnings: compactStrings(warnings),
	}, nil
}

func buildIndexNameMap(names []model.SymbolName) map[string][]string {
	result := make(map[string][]string)
	seen := make(map[string]bool)
	for _, item := range names {
		if item.Class != model.ClassIndex {
			continue
		}
		symbol := normalizeBlockCode(item.Symbol)
		name := strings.TrimSpace(item.Name)
		if symbol == "" || name == "" || seen[symbol+"\x00"+name] {
			continue
		}
		seen[symbol+"\x00"+name] = true
		result[name] = append(result[name], symbol)
	}
	for name := range result {
		sort.Strings(result[name])
	}
	return result
}

func stockSymbolFromIndexMember(code string) (string, bool) {
	code = strings.TrimSpace(strings.ToLower(code))
	if strings.HasPrefix(code, "sh") || strings.HasPrefix(code, "sz") || strings.HasPrefix(code, "bj") {
		if model.ClassifyCode(code) == model.ClassIndex || model.ClassifyCode(code) == model.ClassBlock {
			return "", false
		}
		return code, true
	}
	if !isSixDigitCode(code) {
		return "", false
	}
	symbol, ok := model.SymbolFromCode(code)
	if !ok {
		return "", false
	}
	return symbol, true
}

func normalizeBulkBlockCode(code, market string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	if len(code) != 6 || !isSixDigitCode(code) {
		return ""
	}
	switch strings.TrimSpace(market) {
	case "0":
		return "sz" + code
	case "1":
		return "sh" + code
	case "2":
		return "bj" + code
	}
	if strings.HasPrefix(code, "399") {
		return "sz" + code
	}
	return "sh" + code
}

func marketSymbolFromID(market, code string) string {
	prefix := map[string]string{"0": "sz", "1": "sh", "2": "bj"}[market]
	if prefix == "" || !isSixDigitCode(code) {
		return ""
	}
	return prefix + code
}

func mapValuesSorted(values map[string]model.BlockInfo) []model.BlockInfo {
	result := make([]model.BlockInfo, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sortBlockInfos(result)
	return result
}

func mapMembersSorted(values map[string]model.BlockMember) []model.BlockMember {
	result := make([]model.BlockMember, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sortBlockMembers(result)
	return result
}

func chunkRequests(files []boardFileSync) int {
	total := 0
	for _, file := range files {
		if file.Status != "downloaded" || file.Size <= 0 {
			continue
		}
		total += int((file.Size + 29999) / 30000)
	}
	return total
}
