package calc

import (
	"slices"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

type fakeCalcRepo struct {
	database.DataRepository

	symbols       []string
	gbbq          []model.GbbqData
	klines        map[string][]model.KlineDay
	basics        map[string][]model.BasicDaily
	latestFactors map[string]*model.Factor

	fullBasicCalls         []string
	latestFactorBatches    int
	incrementalBasicsBatch int
	classCalls             [][]string
}

func (f *fakeCalcRepo) GetSymbolsByClass(classes ...string) ([]string, error) {
	f.classCalls = append(f.classCalls, slices.Clone(classes))
	return f.symbols, nil
}

func (f *fakeCalcRepo) GetGbbq() ([]model.GbbqData, error) {
	return f.gbbq, nil
}

func (f *fakeCalcRepo) QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error) {
	rows := f.klines[symbol]
	if startDate == nil {
		return rows, nil
	}
	result := make([]model.KlineDay, 0, len(rows))
	for _, row := range rows {
		if !row.Date.Before(*startDate) && (endDate == nil || !row.Date.After(*endDate)) {
			result = append(result, row)
		}
	}
	return result, nil
}

func (f *fakeCalcRepo) GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error) {
	f.fullBasicCalls = append(f.fullBasicCalls, symbol)
	return f.basics[symbol], nil
}

func (f *fakeCalcRepo) GetLatestBasics(classes ...string) (map[string]model.BasicDaily, error) {
	result := make(map[string]model.BasicDaily)
	for symbol, rows := range f.basics {
		if len(rows) > 0 {
			result[symbol] = rows[len(rows)-1]
		}
	}
	return result, nil
}

func (f *fakeCalcRepo) GetKlineDailyAfterLatestBasic(classes ...string) (map[string][]model.KlineDay, error) {
	latest, _ := f.GetLatestBasics(classes...)
	result := make(map[string][]model.KlineDay)
	for symbol, rows := range f.klines {
		last, ok := latest[symbol]
		if !ok {
			continue
		}
		for _, row := range rows {
			if row.Date.After(last.Date) {
				result[symbol] = append(result[symbol], row)
			}
		}
	}
	return result, nil
}

func (f *fakeCalcRepo) GetLatestFactor(symbol string) (*model.Factor, error) {
	return f.latestFactors[symbol], nil
}

func (f *fakeCalcRepo) GetLatestFactors(classes ...string) (map[string]model.Factor, error) {
	f.latestFactorBatches++
	result := make(map[string]model.Factor)
	for symbol, factor := range f.latestFactors {
		if factor != nil {
			result[symbol] = *factor
		}
	}
	return result, nil
}

func (f *fakeCalcRepo) GetBasicsAfterLatestFactor(classes ...string) (map[string][]model.BasicDaily, error) {
	f.incrementalBasicsBatch++
	result := make(map[string][]model.BasicDaily)
	for symbol, rows := range f.basics {
		factor := f.latestFactors[symbol]
		if factor == nil {
			continue
		}
		for _, row := range rows {
			if !row.Date.Before(factor.Date) {
				result[symbol] = append(result[symbol], row)
			}
		}
	}
	return result, nil
}
