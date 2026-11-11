package calc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

type xdxrInfo struct {
	Fenhong     float64
	Peigu       float64
	Peigujia    float64
	Songzhuangu float64
	// Splitc3: ETF/LOF cat=11 份额折算系数。new_units = old_units * Splitc3,
	// new_NAV = old_NAV / Splitc3。多次折算同日累乘。
	Splitc3 float64
}

type GbbqIndex map[string][]model.GbbqData

type BasicContext struct {
	DB               database.DataRepository
	GbbqIndex        GbbqIndex
	LatestBasics     map[string]model.BasicDaily
	IncrementalKline map[string][]model.KlineDay
}

type BasicExportOptions struct {
	FullRecalc     bool
	Symbols        []string
	RebuildSymbols []string
	FactorCSVPath  string
}

const calcQueryBatchSize = 200

// ExportBasicDailyToCSV 计算并导出 BasicDaily 数据 (覆盖 stock + etf + bstock)。
// Symbols 为 nil 时导出全部 stock/etf/bstock；非 nil 时仅导出指定标的。
func ExportBasicDailyToCSV(
	ctx context.Context,
	db database.DataRepository,
	csvPath string,
	opts BasicExportOptions,
) (int, error) {

	gbbqData, err := db.GetGbbq()
	if err != nil {
		return 0, fmt.Errorf("failed to query gbbq: %w", err)
	}
	gbbqIndex := buildGbbqIndex(gbbqData)

	symbols := opts.Symbols
	if symbols == nil {
		symbols, err = db.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query symbols: %w", err)
		}
	}

	cw, err := utils.NewCSVWriter[model.BasicDaily](csvPath)
	if err != nil {
		return 0, err
	}
	defer cw.Close()

	var factorCw *utils.CSVWriter[model.Factor]
	if opts.FactorCSVPath != "" {
		factorCw, err = utils.NewCSVWriter[model.Factor](opts.FactorCSVPath)
		if err != nil {
			return 0, err
		}
		defer func() {
			if factorCw != nil {
				_ = factorCw.Close()
			}
		}()
	}

	basicCtx := &BasicContext{
		DB:        db,
		GbbqIndex: gbbqIndex,
	}
	if !opts.FullRecalc {
		basicCtx.LatestBasics, err = db.GetLatestBasics(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query latest basics: %w", err)
		}
		basicCtx.IncrementalKline, err = db.GetKlineDailyAfterLatestBasic(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query incremental kline: %w", err)
		}
	}
	rebuildSet := make(map[string]struct{}, len(opts.RebuildSymbols))
	for _, symbol := range opts.RebuildSymbols {
		rebuildSet[symbol] = struct{}{}
	}

	fullSymbols := make([]string, 0, len(symbols))
	incrementalSymbols := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		_, rebuild := rebuildSet[symbol]
		_, hasLatest := basicCtx.LatestBasics[symbol]
		if opts.FullRecalc || rebuild || !hasLatest {
			fullSymbols = append(fullSymbols, symbol)
		} else {
			incrementalSymbols = append(incrementalSymbols, symbol)
		}
	}
	if factorCw != nil && len(incrementalSymbols) > 0 {
		return 0, fmt.Errorf("factor csv requires full recalculation for every symbol")
	}

	rowCount := 0
	for start := 0; start < len(fullSymbols); start += calcQueryBatchSize {
		end := start + calcQueryBatchSize
		if end > len(fullSymbols) {
			end = len(fullSymbols)
		}
		batch := fullSymbols[start:end]
		klineBySymbol, err := db.GetKlineDailyBySymbols(ctx, batch)
		if err != nil {
			return 0, fmt.Errorf("failed to query kline daily batch: %w", err)
		}

		for _, symbol := range batch {
			stockData := klineBySymbol[symbol]
			if len(stockData) == 0 {
				continue
			}

			basics, err := CalculateBasicDaily(stockData, getGbbqBySymbol(basicCtx.GbbqIndex, symbol))
			if err != nil {
				return 0, fmt.Errorf("calc %s failed: %w", symbol, err)
			}
			rows := make([]model.BasicDaily, len(basics))
			for i, basic := range basics {
				rows[i] = *basic
			}
			if err := cw.Write(rows); err != nil {
				return 0, err
			}
			rowCount += len(rows)

			if factorCw != nil {
				if err := factorCw.Write(calculateFullHfq(rows)); err != nil {
					return 0, fmt.Errorf("failed to write precomputed factor csv: %w", err)
				}
			}
		}
		fmt.Printf("basic full batch %d/%d symbols=%d rows=%d\n",
			start/calcQueryBatchSize+1, (len(fullSymbols)+calcQueryBatchSize-1)/calcQueryBatchSize,
			len(batch), rowCount)
	}

	if len(incrementalSymbols) > 0 {
		pipeline := utils.NewPipeline[string, model.BasicDaily]()
		result, err := pipeline.Run(
			ctx,
			incrementalSymbols,
			func(ctx context.Context, symbol string) ([]model.BasicDaily, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}
				return processBasicDaily(basicCtx, symbol, false)
			},
			func(rows []model.BasicDaily) error {
				return cw.Write(rows)
			},
		)
		if err != nil {
			return 0, err
		}
		if result.HasErrors() {
			return 0, fmt.Errorf("export completed with %s", result.ErrorSummary())
		}
		rowCount += int(result.OutputRows)
	}

	if factorCw != nil {
		if err := factorCw.Close(); err != nil {
			return 0, fmt.Errorf("failed to close precomputed factor csv: %w", err)
		}
		factorCw = nil
	}

	return rowCount, nil
}

func processBasicDaily(bc *BasicContext, symbol string, fullRecalc bool) ([]model.BasicDaily, error) {
	if !fullRecalc {
		lastBasic, ok := bc.LatestBasics[symbol]
		if !ok {
			fullRecalc = true
		} else {
			stockData := bc.IncrementalKline[symbol]
			if len(stockData) == 0 {
				return nil, nil
			}

			gbbqs := getGbbqBySymbol(bc.GbbqIndex, symbol)
			basics, err := calculateBasicDailyIncremental(lastBasic, stockData, gbbqs)
			if err != nil {
				return nil, fmt.Errorf("calc incremental %s failed: %w", symbol, err)
			}

			result := make([]model.BasicDaily, len(basics))
			for i, b := range basics {
				result[i] = *b
			}
			return result, nil
		}
	}

	stockData, err := bc.DB.QueryKlineDaily(symbol, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("query stock %s failed: %w", symbol, err)
	}

	if len(stockData) == 0 {
		return nil, nil
	}

	gbbqs := getGbbqBySymbol(bc.GbbqIndex, symbol)

	basics, err := CalculateBasicDaily(stockData, gbbqs)
	if err != nil {
		return nil, fmt.Errorf("calc %s failed: %w", symbol, err)
	}

	result := make([]model.BasicDaily, len(basics))
	for i, b := range basics {
		result[i] = *b
	}

	return result, nil
}

func calculateBasicDailyIncremental(
	last model.BasicDaily,
	stockData []model.KlineDay,
	gbbqData []model.GbbqData,
) ([]*model.BasicDaily, error) {
	if len(stockData) == 0 {
		return nil, nil
	}

	// 把最后一条 basic 作为仅用于承接前收盘和股本状态的种子行，计算后丢弃。
	seed := model.KlineDay{
		Symbol: last.Symbol,
		Date:   last.Date,
		Open:   last.Close,
		High:   last.Close,
		Low:    last.Close,
		Close:  last.Close,
	}
	rows := make([]model.KlineDay, 0, len(stockData)+1)
	rows = append(rows, seed)
	rows = append(rows, stockData...)

	basics, err := CalculateBasicDaily(rows, gbbqData)
	if err != nil {
		return nil, err
	}
	return basics[1:], nil
}

// CalculateBasicDaily 计算 stock/etf/bstock 的 PreClose、涨跌幅、振幅、换手率和市值。
func CalculateBasicDaily(
	stockData []model.KlineDay,
	gbbqData []model.GbbqData,
) ([]*model.BasicDaily, error) {

	if len(stockData) == 0 {
		return []*model.BasicDaily{}, nil
	}

	results := make([]*model.BasicDaily, len(stockData))
	dateMap := make(map[string]int, len(stockData))
	dateFormat := "2006-01-02"

	for i, sd := range stockData {
		dateMap[sd.Date.Format(dateFormat)] = i
	}

	xdxrMap := make(map[int]*xdxrInfo)
	sharesList := make([]model.GbbqData, 0, len(gbbqData))

	// 把 gbbq 事件日期映射到 stockData 索引;非交易日落在记录日的下一个交易日。
	findIdx := func(d time.Time) (int, bool) {
		if i, ok := dateMap[d.Format(dateFormat)]; ok {
			return i, true
		}
		i := sort.Search(len(stockData), func(i int) bool {
			return !stockData[i].Date.Before(d)
		})
		if i < len(stockData) {
			return i, true
		}
		return 0, false
	}

	// 处理 gbbq 事件:
	//   cat=1: 分红/送转股/配股, 调整除权日 PreClose
	//   cat=11: ETF/LOF 份额折算, c3 = 折算系数, PreClose 除以 c3
	//   cat∈{2,3,5,7,8,9,10}: 累积流通/总股本变化 (ETF 一般无此类记录)
	for _, item := range gbbqData {
		switch {
		case item.Category == 1:
			if idx, ok := findIdx(item.Date); ok {
				mergeXdxrFromGbbq(xdxrMap, idx, item)
			}
		case item.Category == 11 && item.C3 > 0:
			if idx, ok := findIdx(item.Date); ok {
				mergeSplitFromGbbq(xdxrMap, idx, item)
			}
		case isShareCategory(item.Category):
			sharesList = append(sharesList, item)
		}
	}

	sort.Slice(sharesList, func(i, j int) bool {
		return sharesList[i].Date.Before(sharesList[j].Date)
	})

	shareIdx := 0
	shareLen := len(sharesList)

	var currentFloat float64 = 0
	var currentTotal float64 = 0

	// gbbq 不一定记录上市时的初始股本，第一条股本变动记录的 C1/C2
	// 携带了"变动前"的流通盘/总股本，可用于回填 stockData 开头到该记录之间的值。
	if shareLen > 0 && sharesList[0].Date.After(stockData[0].Date) {
		first := sharesList[0]
		if first.C1 > 0 {
			currentFloat = first.C1
			currentTotal = first.C2
		} else if first.C3 > 0 {
			currentFloat = first.C3
			currentTotal = first.C4
		} else {
			for _, s := range sharesList {
				if s.C3 > 0 {
					currentFloat = s.C3
					currentTotal = s.C4
					break
				}
			}
		}
	}

	for i, sd := range stockData {
		basic := &model.BasicDaily{
			Date:   sd.Date,
			Symbol: sd.Symbol,
			Close:  sd.Close,
		}

		var prevClose float64
		if i == 0 {
			prevClose = sd.Close
		} else {
			prevClose = stockData[i-1].Close
		}

		basic.PreClose = calculatePreClosePrice(prevClose, xdxrMap[i])

		if basic.PreClose > 0 {
			ChangePercent := (sd.Close - basic.PreClose) / basic.PreClose * 100
			basic.ChangePercent = math.Round(ChangePercent*100) / 100

			amplitude := (sd.High - sd.Low) / basic.PreClose * 100
			basic.Amplitude = math.Round(amplitude*100) / 100
		}

		for shareIdx < shareLen && !sharesList[shareIdx].Date.After(sd.Date) {
			currentFloat = sharesList[shareIdx].C3
			currentTotal = sharesList[shareIdx].C4
			shareIdx++
		}

		if currentFloat > 0 {
			volFloat := float64(sd.Volume)
			val := volFloat / (currentFloat * 10000) * 100
			basic.Turnover = math.Round(val*1000000) / 1000000
			fmv := currentFloat * 10000 * sd.Close
			basic.FloatMV = math.Round(fmv*100) / 100
		}

		if currentTotal > 0 {
			tmv := currentTotal * 10000 * sd.Close
			basic.TotalMV = math.Round(tmv*100) / 100
		}

		results[i] = basic
	}

	return results, nil
}

func buildGbbqIndex(data []model.GbbqData) GbbqIndex {
	index := make(GbbqIndex)
	for _, d := range data {
		index[d.Symbol] = append(index[d.Symbol], d)
	}
	return index
}

func getGbbqBySymbol(index GbbqIndex, symbol string) []model.GbbqData {
	if data, exists := index[symbol]; exists {
		return data
	}
	return []model.GbbqData{}
}

func isShareCategory(cat int) bool {
	switch cat {
	case 2, 3, 5, 7, 8, 9, 10:
		return true
	}
	return false
}

func mergeXdxrFromGbbq(m map[int]*xdxrInfo, idx int, data model.GbbqData) {
	if _, ok := m[idx]; !ok {
		m[idx] = &xdxrInfo{}
	}
	info := m[idx]
	// cat=1 字段顺序: C1=分红, C2=配股价, C3=送转股, C4=配股
	info.Fenhong += data.C1
	info.Songzhuangu += data.C3
	info.Peigu += data.C4
	if data.C2 > 0 {
		info.Peigujia = data.C2
	}
}

// mergeSplitFromGbbq 处理 cat=11 份额折算事件。c3 是折算系数,
// 同日多次折算累乘 (罕见但理论可能)。
func mergeSplitFromGbbq(m map[int]*xdxrInfo, idx int, data model.GbbqData) {
	if _, ok := m[idx]; !ok {
		m[idx] = &xdxrInfo{}
	}
	info := m[idx]
	if info.Splitc3 > 0 {
		info.Splitc3 *= data.C3
	} else {
		info.Splitc3 = data.C3
	}
}

func calculatePreClosePrice(prevClose float64, info *xdxrInfo) float64 {
	if info == nil {
		return prevClose
	}
	denominator := 10 + info.Peigu + info.Songzhuangu
	if denominator <= 0 {
		return prevClose
	}
	numerator := (prevClose*10 - info.Fenhong) + (info.Peigu * info.Peigujia)
	price := numerator / denominator
	if info.Splitc3 > 0 {
		price /= info.Splitc3
	}
	if !isPositiveFinite(price) {
		return prevClose
	}
	return price
}
