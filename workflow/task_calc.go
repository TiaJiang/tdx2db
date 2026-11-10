package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/jing2uo/tdx2db/calc"
	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

var (
	TaskCalcBasic  *Task
	TaskCalcFactor *Task
)

func init() {
	TaskCalcBasic = &Task{
		Name:      "calc_basic",
		DependsOn: []string{"update_daily", "update_gbbq"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedBasic }),
		Executor:  executeCalcBasic,
	}
	registerTask(TaskCalcBasic, "update")

	TaskCalcFactor = &Task{
		Name:      "calc_factor",
		DependsOn: []string{"calc_basic"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedFactor }),
		Executor:  executeCalcFactor,
	}
	registerTask(TaskCalcFactor, "update")
}

func executeCalcBasic(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("📟 计算股票基础行情")
	gbbqChanged, _ := args.Extra[ExtraGbbqChanged].(bool)
	changedSymbols, _ := args.Extra[ExtraGbbqChangedSymbols].([]string)

	eligibleSymbols, err := db.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
	if err != nil {
		return nil, fmt.Errorf("failed to query calculation symbols: %w", err)
	}
	dailyLatest, err := db.GetLatestDates(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest daily dates: %w", err)
	}
	basicLatest, err := db.GetLatestDates(model.TableBasicDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest basic dates: %w", err)
	}

	fullRecalc := gbbqChanged && len(changedSymbols) == 0
	symbols := selectBasicSymbols(dailyLatest, basicLatest, eligibleSymbols, changedSymbols, fullRecalc)
	if len(symbols) == 0 {
		fmt.Println("🌲 股票基础行情无需更新")
		return &TaskResult{State: StateSkipped, Message: "basic data is current"}, nil
	}

	basicCSV := filepath.Join(args.TempDir, "basics.csv")
	rebuildSymbols := intersectSymbols(symbols, changedSymbols)
	if fullRecalc {
		rebuildSymbols = nil
	}

	rowCount, err := calc.ExportBasicDailyToCSV(ctx, db, basicCSV, calc.BasicExportOptions{
		FullRecalc:     fullRecalc,
		Symbols:        symbols,
		RebuildSymbols: rebuildSymbols,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to export basic to csv: %w", err)
	}

	if rowCount == 0 {
		fmt.Println("🌲 股票基础行情无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new basic data"}, nil
	}

	if fullRecalc {
		if err := db.TruncateTable(model.TableBasicDaily); err != nil {
			return nil, fmt.Errorf("failed to clear basic table: %w", err)
		}
	}
	if err := db.ImportBasic(basicCSV, rebuildSymbols); err != nil {
		return nil, fmt.Errorf("failed to import basic data: %w", err)
	}
	fmt.Printf("🔢 基础行情导入成功 (symbols=%d, rows=%d, rebuild=%d, full=%t)\n",
		len(symbols), rowCount, len(rebuildSymbols), fullRecalc)
	return &TaskResult{
		State: StateCompleted,
		Rows:  rowCount,
		Message: fmt.Sprintf("basic data calculated (symbols=%d, rows=%d, rebuild=%d, full=%t)",
			len(symbols), rowCount, len(rebuildSymbols), fullRecalc),
	}, nil
}

func executeCalcFactor(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("📟 计算股票复权因子")
	factorCSV := filepath.Join(args.TempDir, "factor.csv")
	gbbqChanged, _ := args.Extra[ExtraGbbqChanged].(bool)
	changedSymbols, _ := args.Extra[ExtraGbbqChangedSymbols].([]string)
	eligibleSymbols, err := db.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
	if err != nil {
		return nil, fmt.Errorf("failed to query factor symbols: %w", err)
	}
	invalidSymbols, err := db.GetInvalidFactorSymbols()
	if err != nil {
		return nil, err
	}
	fullRecalc := gbbqChanged && len(changedSymbols) == 0
	rebuildSymbols := intersectSymbols(eligibleSymbols, mergeSymbols(changedSymbols, invalidSymbols))

	symbols := eligibleSymbols
	if !fullRecalc {
		symbols, err = selectFactorSymbols(db, eligibleSymbols, rebuildSymbols)
		if err != nil {
			return nil, err
		}
		if len(symbols) == 0 {
			fmt.Println("🌲 复权因子无需更新")
			return &TaskResult{State: StateSkipped, Message: "factor data is current"}, nil
		}
	}

	opts := calc.FactorExportOptions{
		FullRecalc:     fullRecalc,
		Symbols:        symbols,
		RebuildSymbols: rebuildSymbols,
	}
	factorCount, err := calc.ExportFactorsToCSV(ctx, db, factorCSV, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to export factor to csv: %w", err)
	}

	if factorCount == 0 {
		fmt.Println("🌲 复权因子无需更新")
		return &TaskResult{State: StateSkipped, Message: "no new factor data"}, nil
	}

	if fullRecalc {
		if err := db.TruncateTable(model.TableAdjustFactor); err != nil {
			return nil, fmt.Errorf("failed to clear factor table: %w", err)
		}
		rebuildSymbols = nil
	}
	if err := db.ImportAdjustFactors(factorCSV, rebuildSymbols); err != nil {
		return nil, fmt.Errorf("failed to append factor data: %w", err)
	}
	fmt.Printf("🔢 复权因子导入成功 (symbols=%d, rows=%d, rebuild=%d, full=%t)\n",
		len(symbols), factorCount, len(rebuildSymbols), fullRecalc)
	return &TaskResult{
		State: StateCompleted,
		Rows:  factorCount,
		Message: fmt.Sprintf("factors calculated (symbols=%d, rows=%d, rebuild=%d, full=%t)",
			len(symbols), factorCount, len(rebuildSymbols), fullRecalc),
	}, nil
}

func selectFactorSymbols(
	db database.DataRepository,
	eligibleSymbols []string,
	rebuildSymbols []string,
) ([]string, error) {
	basicLatest, err := db.GetLatestDates(model.TableBasicDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest basic dates: %w", err)
	}
	factorLatest, err := db.GetLatestDates(model.TableAdjustFactor.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest factor dates: %w", err)
	}

	selected := make(map[string]struct{}, len(rebuildSymbols))
	for _, symbol := range rebuildSymbols {
		selected[symbol] = struct{}{}
	}
	for _, symbol := range eligibleSymbols {
		basicDate, hasBasic := basicLatest[symbol]
		if !hasBasic {
			continue
		}
		factorDate, hasFactor := factorLatest[symbol]
		if !hasFactor || dateBefore(factorDate, basicDate) {
			selected[symbol] = struct{}{}
		}
	}

	symbols := make([]string, 0, len(selected))
	for _, symbol := range eligibleSymbols {
		if _, ok := selected[symbol]; ok {
			symbols = append(symbols, symbol)
		}
	}
	return symbols, nil
}

func selectBasicSymbols(
	dailyLatest map[string]time.Time,
	basicLatest map[string]time.Time,
	eligibleSymbols []string,
	changedSymbols []string,
	fullRecalc bool,
) []string {
	eligible := make(map[string]struct{}, len(eligibleSymbols))
	for _, symbol := range eligibleSymbols {
		eligible[symbol] = struct{}{}
	}

	selected := make(map[string]struct{})
	if fullRecalc {
		for symbol := range eligible {
			selected[symbol] = struct{}{}
		}
	} else {
		for _, symbol := range changedSymbols {
			if _, ok := eligible[symbol]; !ok {
				continue
			}
			if _, ok := dailyLatest[symbol]; ok {
				selected[symbol] = struct{}{}
			}
		}
		for symbol, dailyDate := range dailyLatest {
			if _, ok := eligible[symbol]; !ok {
				continue
			}
			basicDate, ok := basicLatest[symbol]
			if !ok || dateBefore(basicDate, dailyDate) {
				selected[symbol] = struct{}{}
			}
		}
	}

	symbols := make([]string, 0, len(selected))
	for symbol := range selected {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func mergeSymbols(groups ...[]string) []string {
	set := make(map[string]struct{})
	for _, group := range groups {
		for _, symbol := range group {
			if symbol != "" {
				set[symbol] = struct{}{}
			}
		}
	}
	symbols := make([]string, 0, len(set))
	for symbol := range set {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}

func intersectSymbols(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, symbol := range right {
		rightSet[symbol] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, symbol := range left {
		if _, ok := rightSet[symbol]; ok {
			result = append(result, symbol)
		}
	}
	return result
}

func dateBefore(a, b time.Time) bool {
	return a.Format("2006-01-02") < b.Format("2006-01-02")
}
