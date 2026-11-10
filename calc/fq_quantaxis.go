package calc

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

// FactorContext 处理上下文
type FactorContext struct {
	DB               database.DataRepository
	LatestFactors    map[string]model.Factor
	IncrementalBasic map[string][]model.BasicDaily
}

type FactorExportOptions struct {
	FullRecalc     bool
	Symbols        []string
	RebuildSymbols []string
}

// ExportFactorsToCSV 导出 stock/etf/bstock 后复权因子（每天一条记录，和日线对齐）。
// FullRecalc=true 时全部标的历史重算；否则 RebuildSymbols 中的标的历史重算，
// 其余标的仅追加各自最新因子日期之后的新记录。
func ExportFactorsToCSV(
	ctx context.Context,
	db database.DataRepository,
	csvPath string,
	opts FactorExportOptions,
) (int, error) {
	symbols := opts.Symbols
	if symbols == nil {
		var err error
		symbols, err = db.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query symbols: %w", err)
		}
	}

	if len(symbols) == 0 {
		return 0, nil
	}

	cw, err := utils.NewCSVWriter[model.Factor](csvPath)
	if err != nil {
		return 0, err
	}
	defer cw.Close()

	fctx := &FactorContext{
		DB: db,
	}
	if !opts.FullRecalc {
		fctx.LatestFactors, err = db.GetLatestFactors(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query latest factors: %w", err)
		}
		fctx.IncrementalBasic, err = db.GetBasicsAfterLatestFactor(model.ClassStock, model.ClassETF, model.ClassBStock)
		if err != nil {
			return 0, fmt.Errorf("failed to query basics after latest factor: %w", err)
		}
	}
	rebuildSet := make(map[string]struct{}, len(opts.RebuildSymbols))
	for _, symbol := range opts.RebuildSymbols {
		rebuildSet[symbol] = struct{}{}
	}

	pipeline := utils.NewPipeline[string, model.Factor]()

	result, err := pipeline.Run(
		ctx,
		symbols,
		func(ctx context.Context, symbol string) ([]model.Factor, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			_, rebuild := rebuildSet[symbol]
			return processFactorSymbol(fctx, symbol, opts.FullRecalc || rebuild)
		},
		func(rows []model.Factor) error {
			return cw.Write(rows)
		},
	)

	if err != nil {
		return 0, err
	}

	if result.HasErrors() {
		return 0, fmt.Errorf("export completed with %s", result.ErrorSummary())
	}

	return int(result.OutputRows), nil
}

func processFactorSymbol(fctx *FactorContext, symbol string, fullRecalc bool) ([]model.Factor, error) {
	if fullRecalc {
		basics, err := fctx.DB.GetBasicsBySymbol(symbol)
		if err != nil {
			return nil, fmt.Errorf("query %s failed: %w", symbol, err)
		}
		return calculateFullHfq(basics), nil
	}

	last, ok := fctx.LatestFactors[symbol]
	if !ok {
		basics, err := fctx.DB.GetBasicsBySymbol(symbol)
		if err != nil {
			return nil, fmt.Errorf("query %s failed: %w", symbol, err)
		}
		return calculateFullHfq(basics), nil
	}

	basics := fctx.IncrementalBasic[symbol]
	if len(basics) == 0 {
		return nil, nil
	}

	factors, ok := calculateIncrementalHfq(basics, last)
	if !ok {
		basics, err := fctx.DB.GetBasicsBySymbol(symbol)
		if err != nil {
			return nil, fmt.Errorf("query full basics for %s failed: %w", symbol, err)
		}
		return calculateFullHfq(basics), nil
	}
	return factors, nil
}

// calculateFullHfq 全量计算 HFQ（每天一条记录）
// basic.PreClose 已包含除权调整，prevClose != PreClose 即为除权日，无需独立的 xdxr 日期集合
func calculateFullHfq(basics []model.BasicDaily) []model.Factor {
	if len(basics) == 0 {
		return nil
	}

	results := make([]model.Factor, 0, len(basics))
	currentHfq := 1.0
	prevClose := basics[0].Close

	// 第一天
	results = append(results, model.Factor{
		Symbol:    basics[0].Symbol,
		Date:      basics[0].Date,
		HfqFactor: currentHfq,
	})

	// 后续每天：通过 prevClose / PreClose 检测除权
	for i := 1; i < len(basics); i++ {
		basic := basics[i]
		if basic.PreClose > 0 && math.IsInf(basic.PreClose, 0) == false {
			ratio := prevClose / basic.PreClose
			if isPositiveFinite(ratio) && !floatEqual(ratio, 1.0) {
				nextHfq := currentHfq * ratio
				if isPositiveFinite(nextHfq) {
					currentHfq = nextHfq
				}
			}
		}
		results = append(results, model.Factor{
			Symbol:    basic.Symbol,
			Date:      basic.Date,
			HfqFactor: currentHfq,
		})
		prevClose = basic.Close
	}

	return results
}

// calculateIncrementalHfq 从库内最后一个因子继续计算。
// 返回 false 表示最后一个因子日期不在当前基础行情中，需要按标的全量重算。
func calculateIncrementalHfq(basics []model.BasicDaily, last model.Factor) ([]model.Factor, bool) {
	if len(basics) == 0 || last.HfqFactor <= 0 || !isPositiveFinite(last.HfqFactor) {
		return nil, false
	}

	idx := sort.Search(len(basics), func(i int) bool {
		return !basics[i].Date.Before(last.Date)
	})
	if idx >= len(basics) || !basics[idx].Date.Equal(last.Date) {
		return nil, false
	}
	if idx == len(basics)-1 {
		return nil, true
	}

	results := make([]model.Factor, 0, len(basics)-idx-1)
	currentHfq := last.HfqFactor
	prevClose := basics[idx].Close

	for i := idx + 1; i < len(basics); i++ {
		basic := basics[i]
		if basic.PreClose > 0 && isPositiveFinite(basic.PreClose) {
			ratio := prevClose / basic.PreClose
			if isPositiveFinite(ratio) && !floatEqual(ratio, 1.0) {
				nextHfq := currentHfq * ratio
				if isPositiveFinite(nextHfq) {
					currentHfq = nextHfq
				}
			}
		}
		results = append(results, model.Factor{
			Symbol:    basic.Symbol,
			Date:      basic.Date,
			HfqFactor: currentHfq,
		})
		prevClose = basic.Close
	}
	return results, true
}

func isPositiveFinite(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func floatEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
