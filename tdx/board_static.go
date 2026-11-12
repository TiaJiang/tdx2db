package tdx

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jing2uo/tdx2db/model"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	blockTypeIndustry = "industry"
	blockTypeConcept  = "concept"
	blockTypeStyle    = "style"
	blockTypeRegion   = "region"
	blockTypeIndex    = "index"
)

type boardDefinition struct {
	Info         model.BlockInfo
	IndustryCode string
	Group        string
}

type staticBoardData struct {
	Blocks              []model.BlockInfo
	Members             []model.BlockMember
	Warnings            []string
	PreserveAllNonIndex bool
	Preserve881         bool
	Preserve880Members  bool
	RegionFallback      bool
	PreserveRegion      bool
}

func loadStaticBoardData(ctx context.Context, dir string) (*staticBoardData, error) {
	result := &staticBoardData{}
	if strings.TrimSpace(dir) == "" {
		result.PreserveAllNonIndex = true
		result.Preserve881 = true
		result.Warnings = append(result.Warnings,
			"board-data-dir 未配置，保留旧的非指数板块；F10 仅更新数据库已有指数")
		return result, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			result.PreserveAllNonIndex = true
			result.Preserve881 = true
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("board-data-dir 不存在: %s；保留旧的非指数板块", dir))
			return result, nil
		}
		return nil, fmt.Errorf("failed to stat board data dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("board data path is not a directory: %s", dir)
	}

	allDefinitions := make([]boardDefinition, 0)
	tdxzsPath := filepath.Join(dir, "tdxzs.cfg")
	tdxzs3Path := filepath.Join(dir, "tdxzs3.cfg")

	tdxzsDefinitions, found, err := parseBoardCatalogFile(ctx, tdxzsPath, tdxzsPath)
	if err != nil {
		return nil, err
	}
	if found {
		allDefinitions = append(allDefinitions, tdxzsDefinitions...)
	} else {
		result.PreserveAllNonIndex = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("缺少 %s；保留旧的非指数板块", tdxzsPath))
	}

	tdxzs3Definitions, found, err := parseBoardCatalogFile(ctx, tdxzs3Path, tdxzs3Path)
	if err != nil {
		return nil, err
	}
	if found {
		allDefinitions = append(allDefinitions, tdxzs3Definitions...)
	} else {
		result.Preserve881 = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("缺少 %s；保留旧的 881 行业板块", tdxzs3Path))
	}

	allDefinitions = assignIndustryParents(allDefinitions)
	result.Blocks, result.Warnings = mergeBoardDefinitions(allDefinitions, result.Warnings)

	tdxhyPath := filepath.Join(dir, "tdxhy.cfg")
	tdxhyMembers, found, warnings, err := parseTDXHY(ctx, tdxhyPath, allDefinitions)
	if err != nil {
		return nil, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	if found {
		result.Members = append(result.Members, tdxhyMembers...)
	} else {
		result.Preserve880Members = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("缺少 %s；保留旧的 880 行业成分", tdxhyPath))
	}

	baseDBFPath := filepath.Join(dir, "base.dbf")
	regionMembers, found, warnings, err := parseRegionDBF(ctx, baseDBFPath, allDefinitions)
	if err != nil {
		result.PreserveRegion = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("解析 %s 失败，保留旧地区成分: %v", baseDBFPath, err))
	} else {
		result.Warnings = append(result.Warnings, warnings...)
		if found {
			result.Members = append(result.Members, regionMembers...)
		} else {
			result.RegionFallback = true
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("缺少 %s；地区板块将使用 F10 兜底", baseDBFPath))
		}
	}

	return result, nil
}

func parseBoardCatalogFile(ctx context.Context, path, group string) ([]boardDefinition, bool, error) {
	text, found, err := readBoardConfig(path)
	if err != nil || !found {
		return nil, found, err
	}

	var definitions []boardDefinition
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, true, err
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		code := strings.TrimSpace(fields[1])
		category, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil || name == "" || !isSixDigitCode(code) {
			continue
		}
		blockType, ok := staticBlockType(category)
		if !ok {
			continue
		}
		blockCode := normalizeBlockCode(code)
		industryCode := ""
		level := 1
		if len(fields) > 5 {
			industryCode = strings.ToUpper(strings.TrimSpace(fields[5]))
		}
		if blockType == blockTypeIndustry {
			level = initialIndustryLevel(industryCode)
		}
		definitions = append(definitions, boardDefinition{
			Info: model.BlockInfo{
				BlockType:   blockType,
				BlockName:   name,
				BlockSymbol: blockCode,
				BlockCode:   blockCode,
				BlockLevel:  level,
			},
			IndustryCode: industryCode,
			Group:        group,
		})
	}
	return definitions, true, nil
}

func staticBlockType(category int) (string, bool) {
	switch category {
	case 2, 12:
		return blockTypeIndustry, true
	case 3:
		return blockTypeRegion, true
	case 4:
		return blockTypeConcept, true
	case 5:
		return blockTypeStyle, true
	default:
		return "", false
	}
}

func assignIndustryParents(definitions []boardDefinition) []boardDefinition {
	sort.SliceStable(definitions, func(i, j int) bool {
		if definitions[i].Group != definitions[j].Group {
			return definitions[i].Group < definitions[j].Group
		}
		if len(definitions[i].IndustryCode) != len(definitions[j].IndustryCode) {
			return len(definitions[i].IndustryCode) < len(definitions[j].IndustryCode)
		}
		return definitions[i].Info.BlockCode < definitions[j].Info.BlockCode
	})

	for i := range definitions {
		if definitions[i].Info.BlockType != blockTypeIndustry {
			definitions[i].Info.BlockLevel = 1
			continue
		}
		parentIndex := -1
		for j := 0; j < i; j++ {
			if definitions[j].Group != definitions[i].Group ||
				definitions[j].Info.BlockType != blockTypeIndustry {
				continue
			}
			if industryNamespace(definitions[j].IndustryCode) != industryNamespace(definitions[i].IndustryCode) {
				continue
			}
			if len(definitions[j].IndustryCode) >= len(definitions[i].IndustryCode) ||
				!strings.HasPrefix(definitions[i].IndustryCode, definitions[j].IndustryCode) {
				continue
			}
			if parentIndex < 0 || len(definitions[j].IndustryCode) > len(definitions[parentIndex].IndustryCode) {
				parentIndex = j
			}
		}
		if parentIndex >= 0 {
			definitions[i].Info.ParentCode = definitions[parentIndex].Info.BlockCode
			definitions[i].Info.BlockLevel = definitions[parentIndex].Info.BlockLevel + 1
		}
	}
	return definitions
}

func mergeBoardDefinitions(definitions []boardDefinition, warnings []string) ([]model.BlockInfo, []string) {
	byCode := make(map[string]model.BlockInfo, len(definitions))
	for _, definition := range definitions {
		info := definition.Info
		existing, exists := byCode[info.BlockCode]
		if !exists {
			byCode[info.BlockCode] = info
			continue
		}
		if existing != info {
			warnings = append(warnings,
				fmt.Sprintf("板块目录重复且内容冲突，保留首条: %s", info.BlockCode))
		}
	}

	blocks := make([]model.BlockInfo, 0, len(byCode))
	for _, info := range byCode {
		if info.BlockType == blockTypeIndustry && info.BlockLevel < 1 {
			info.BlockLevel = 1
		}
		blocks = append(blocks, info)
	}
	sortBlockInfos(blocks)
	return blocks, warnings
}

func parseTDXHY(ctx context.Context, path string, definitions []boardDefinition) ([]model.BlockMember, bool, []string, error) {
	text, found, err := readBoardConfig(path)
	if err != nil || !found {
		return nil, found, nil, err
	}

	var industries []boardDefinition
	for _, definition := range definitions {
		if definition.Info.BlockType == blockTypeIndustry && definition.IndustryCode != "" {
			industries = append(industries, definition)
		}
	}

	memberSet := make(map[string]model.BlockMember)
	unmatchedT := 0
	unmatchedX := 0
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, true, nil, err
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 6 {
			continue
		}
		market := strings.TrimSpace(fields[0])
		stockSymbol := normalizeMarketSymbol(market, strings.TrimSpace(fields[1]))
		if stockSymbol == "" {
			continue
		}
		for _, item := range []struct {
			code      string
			namespace string
			unmatched *int
		}{
			{code: strings.ToUpper(strings.TrimSpace(fields[2])), namespace: "T", unmatched: &unmatchedT},
			{code: strings.ToUpper(strings.TrimSpace(fields[5])), namespace: "X", unmatched: &unmatchedX},
		} {
			if item.code == "" {
				continue
			}
			matched := false
			for _, definition := range industries {
				if industryNamespace(definition.IndustryCode) != item.namespace ||
					!strings.HasPrefix(item.code, definition.IndustryCode) {
					continue
				}
				matched = true
				member := model.BlockMember{
					StockSymbol: stockSymbol,
					BlockCode:   definition.Info.BlockCode,
				}
				memberSet[member.StockSymbol+"\x00"+member.BlockCode] = member
			}
			if !matched {
				*item.unmatched++
			}
		}
	}

	members := make([]model.BlockMember, 0, len(memberSet))
	for _, member := range memberSet {
		members = append(members, member)
	}
	sortBlockMembers(members)

	var warnings []string
	if unmatchedT > 0 {
		warnings = append(warnings, fmt.Sprintf("tdxhy.cfg: %d 个 T 行业代码未匹配 880 板块", unmatchedT))
	}
	if unmatchedX > 0 {
		warnings = append(warnings, fmt.Sprintf("tdxhy.cfg: %d 个 X 行业代码未匹配 881 板块", unmatchedX))
	}
	return members, true, warnings, nil
}

func parseRegionDBF(ctx context.Context, path string, definitions []boardDefinition) ([]model.BlockMember, bool, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil, nil
		}
		return nil, false, nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if len(data) < 32 {
		return nil, true, nil, fmt.Errorf("DBF header is incomplete")
	}

	count := int(binary.LittleEndian.Uint32(data[4:8]))
	headerSize := int(binary.LittleEndian.Uint16(data[8:10]))
	recordSize := int(binary.LittleEndian.Uint16(data[10:12]))
	if headerSize < 33 || recordSize < 1 || headerSize > len(data) {
		return nil, true, nil, fmt.Errorf("invalid DBF header size=%d record size=%d", headerSize, recordSize)
	}
	if headerSize+count*recordSize > len(data) {
		return nil, true, nil, fmt.Errorf("DBF records are truncated")
	}

	regionCodes := make(map[string]string)
	for _, definition := range definitions {
		if definition.Info.BlockType != blockTypeRegion {
			continue
		}
		key := strings.TrimSpace(definition.IndustryCode)
		if key != "" {
			regionCodes[key] = definition.Info.BlockCode
		}
	}
	if len(regionCodes) == 0 {
		return nil, true, []string{"base.dbf 未匹配到地区板块定义"}, nil
	}

	fields := make(map[string]struct {
		offset int
		size   int
	})
	offset := 1
	for pos := 32; pos+32 <= headerSize-1; pos += 32 {
		if data[pos] == 0x0d {
			break
		}
		name := strings.TrimRight(string(data[pos:pos+11]), "\x00")
		size := int(data[pos+16])
		fields[name] = struct {
			offset int
			size   int
		}{offset: offset, size: size}
		offset += size
	}
	fieldValue := func(record []byte, name string) string {
		field, ok := fields[name]
		if !ok || field.offset+field.size > len(record) {
			return ""
		}
		return strings.TrimSpace(string(record[field.offset : field.offset+field.size]))
	}

	memberSet := make(map[string]model.BlockMember)
	for index := 0; index < count; index++ {
		if index%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, true, nil, err
			}
		}
		start := headerSize + index*recordSize
		record := data[start : start+recordSize]
		if len(record) == 0 || record[0] == '*' {
			continue
		}
		blockCode := regionCodes[fieldValue(record, "DY")]
		code := fieldValue(record, "GPDM")
		symbol := normalizeMarketSymbol(fieldValue(record, "SC"), code)
		if blockCode == "" || symbol == "" {
			continue
		}
		member := model.BlockMember{StockSymbol: symbol, BlockCode: blockCode}
		memberSet[symbol+"\x00"+blockCode] = member
	}

	members := make([]model.BlockMember, 0, len(memberSet))
	for _, member := range memberSet {
		members = append(members, member)
	}
	sortBlockMembers(members)
	return members, true, nil, nil
}

func readBoardConfig(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to read board config %s: %w", path, err)
	}
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	if utf8.Valid(data) {
		return string(data), true, nil
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		return "", false, fmt.Errorf("failed to decode board config %s as GB18030: %w", path, err)
	}
	return string(decoded), true, nil
}

func initialIndustryLevel(code string) int {
	switch industryNamespace(code) {
	case "T":
		if len(code) <= 5 {
			return 1
		}
		return (len(code) - 3) / 2
	case "X":
		if len(code) <= 3 {
			return 1
		}
		return (len(code) - 1) / 2
	default:
		return 1
	}
}

func industryNamespace(code string) string {
	if code == "" {
		return ""
	}
	return code[:1]
}

func normalizeBlockCode(code string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	if strings.HasPrefix(code, "sh") || strings.HasPrefix(code, "sz") || strings.HasPrefix(code, "bj") {
		return code
	}
	if len(code) != 6 {
		return code
	}
	return "sh" + code
}

func normalizeMarketSymbol(market, code string) string {
	code = strings.TrimSpace(strings.ToLower(code))
	if strings.HasPrefix(code, "sh") || strings.HasPrefix(code, "sz") || strings.HasPrefix(code, "bj") {
		return code
	}
	if !isSixDigitCode(code) {
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

	switch {
	case strings.HasPrefix(code, "880"), strings.HasPrefix(code, "881"),
		strings.HasPrefix(code, "399"):
		if strings.HasPrefix(code, "399") {
			return "sz" + code
		}
		return "sh" + code
	case strings.HasPrefix(code, "899"), strings.HasPrefix(code, "92"):
		return "bj" + code
	case strings.HasPrefix(code, "000"):
		return "sh" + code
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		return "bj" + code
	case strings.HasPrefix(code, "6"), strings.HasPrefix(code, "9"):
		return "sh" + code
	default:
		return "sz" + code
	}
}

func isSixDigitCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
