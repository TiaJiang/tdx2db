package calc

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func date(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

// TestXdxrFactorUpdate 验证除权日 prevClose != PreClose 时因子正确更新
// 场景：送股导致 preclose 被调整，factor 应乘以 prevClose/preclose
func TestXdxrFactorUpdate(t *testing.T) {
	// basic.PreClose 已由 calc/basic.go 正确计算（含非交易日映射）
	// calculateFullHfq 只需检测 prevClose != PreClose
	basics := []model.BasicDaily{
		{Symbol: "sz000001", Date: date(2007, 5, 31), Close: 28.69, PreClose: 27.32},
		// 2007-06-18 除权（非交易日），basic.go 已映射到 2007-06-20 并调整 PreClose
		{Symbol: "sz000001", Date: date(2007, 6, 20), Close: 31.19, PreClose: 26.081818181818186},
		{Symbol: "sz000001", Date: date(2007, 6, 21), Close: 34.31, PreClose: 31.19},
	}

	factors := calculateFullHfq(basics)

	if len(factors) != 3 {
		t.Fatalf("expected 3 factors, got %d", len(factors))
	}

	// 第一天：hfq = 1.0
	if factors[0].HfqFactor != 1.0 {
		t.Errorf("day 0: expected hfq=1.0, got %f", factors[0].HfqFactor)
	}

	// 2007-06-20：prevClose=28.69, preclose=26.08..., ratio≈1.1
	expectedHfq := 28.69 / 26.081818181818186
	if math.Abs(factors[1].HfqFactor-expectedHfq) > 1e-9 {
		t.Errorf("day 1 (2007-06-20): expected hfq≈%f, got %f", expectedHfq, factors[1].HfqFactor)
	}

	// 2007-06-21：无除权，hfq 不变
	if math.Abs(factors[2].HfqFactor-expectedHfq) > 1e-9 {
		t.Errorf("day 2 (2007-06-21): expected hfq≈%f, got %f", expectedHfq, factors[2].HfqFactor)
	}
}

// TestNoXdxrFactorUnchanged 验证无除权时因子保持不变
func TestNoXdxrFactorUnchanged(t *testing.T) {
	basics := []model.BasicDaily{
		{Symbol: "sz000001", Date: date(2020, 7, 20), Close: 15.0, PreClose: 14.5},
		{Symbol: "sz000001", Date: date(2020, 7, 21), Close: 16.0, PreClose: 15.0},
		{Symbol: "sz000001", Date: date(2020, 7, 22), Close: 15.5, PreClose: 16.0},
	}

	factors := calculateFullHfq(basics)

	for i, f := range factors {
		if f.HfqFactor != 1.0 {
			t.Errorf("day %d: expected hfq=1.0, got %f", i, f.HfqFactor)
		}
	}
}

func TestIncrementalHfqContinuesFromLastFactor(t *testing.T) {
	basics := []model.BasicDaily{
		{Symbol: "sz000001", Date: date(2026, 1, 2), Close: 10, PreClose: 10},
		{Symbol: "sz000001", Date: date(2026, 1, 5), Close: 11, PreClose: 10},
		{Symbol: "sz000001", Date: date(2026, 1, 6), Close: 5, PreClose: 5.5},
	}
	last := model.Factor{
		Symbol:    "sz000001",
		Date:      date(2026, 1, 5),
		HfqFactor: 2,
	}

	factors, ok := calculateIncrementalHfq(basics, last)
	if !ok {
		t.Fatal("calculateIncrementalHfq() unexpectedly requested full recalculation")
	}
	if len(factors) != 1 {
		t.Fatalf("len(factors) = %d, want 1", len(factors))
	}
	want := 2 * 11 / 5.5
	if math.Abs(factors[0].HfqFactor-want) > 1e-9 {
		t.Fatalf("hfq = %v, want %v", factors[0].HfqFactor, want)
	}
}

func TestIncrementalHfqRequiresKnownLastDate(t *testing.T) {
	basics := []model.BasicDaily{
		{Symbol: "sz000001", Date: date(2026, 1, 5), Close: 11, PreClose: 10},
	}
	last := model.Factor{
		Symbol:    "sz000001",
		Date:      date(2026, 1, 2),
		HfqFactor: 2,
	}

	if _, ok := calculateIncrementalHfq(basics, last); ok {
		t.Fatal("calculateIncrementalHfq() should request full recalculation")
	}
}

func TestFullHfqIgnoresInvalidPreClose(t *testing.T) {
	basics := []model.BasicDaily{
		{Symbol: "sz000001", Date: date(2026, 1, 2), Close: 10, PreClose: 10},
		{Symbol: "sz000001", Date: date(2026, 1, 5), Close: 10, PreClose: -1},
	}

	factors := calculateFullHfq(basics)
	if factors[1].HfqFactor != 1 {
		t.Fatalf("hfq = %v, want 1", factors[1].HfqFactor)
	}
}

func TestExportFactorsSelectiveRebuild(t *testing.T) {
	day1 := date(2026, 1, 2)
	day2 := date(2026, 1, 5)
	repo := &fakeCalcRepo{
		symbols: []string{"sh600000", "sz000001"},
		basics: map[string][]model.BasicDaily{
			"sh600000": {
				{Symbol: "sh600000", Date: day1, Close: 10, PreClose: 10},
				{Symbol: "sh600000", Date: day2, Close: 11, PreClose: 10},
			},
			"sz000001": {
				{Symbol: "sz000001", Date: day1, Close: 20, PreClose: 20},
				{Symbol: "sz000001", Date: day2, Close: 21, PreClose: 20},
			},
		},
		latestFactors: map[string]*model.Factor{
			"sz000001": {Symbol: "sz000001", Date: day1, HfqFactor: 1},
		},
	}
	path := filepath.Join(t.TempDir(), "factor.csv")

	count, err := ExportFactorsToCSV(context.Background(), repo, path, FactorExportOptions{
		RebuildSymbols: []string{"sh600000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	rows := readCSVFile(t, path)
	counts := map[string]int{}
	for _, row := range rows[1:] {
		counts[row[0]]++
	}
	if counts["sh600000"] != 2 {
		t.Fatalf("sh600000 rows = %d, want full rebuild 2", counts["sh600000"])
	}
	if counts["sz000001"] != 1 {
		t.Fatalf("sz000001 rows = %d, want incremental 1", counts["sz000001"])
	}
	if len(repo.fullBasicCalls) != 0 {
		t.Fatalf("unexpected per-symbol basic calls: %v", repo.fullBasicCalls)
	}
	if !reflect.DeepEqual(repo.basicBatchCalls, [][]string{{"sh600000"}}) {
		t.Fatalf("basic batch calls = %v, want [[sh600000]]", repo.basicBatchCalls)
	}
	if repo.latestFactorBatches != 1 || repo.incrementalBasicsBatch != 1 {
		t.Fatalf("batch calls = latest factors %d, incremental basics %d; want 1, 1",
			repo.latestFactorBatches, repo.incrementalBasicsBatch)
	}
	wantClasses := []string{model.ClassStock, model.ClassETF, model.ClassBStock}
	if len(repo.classCalls) != 1 || !reflect.DeepEqual(repo.classCalls[0], wantClasses) {
		t.Fatalf("class calls = %v, want %v", repo.classCalls, wantClasses)
	}
}

func TestExportFactorsFullUsesBatchedQueries(t *testing.T) {
	day := date(2026, 1, 5)
	symbols := make([]string, calcQueryBatchSize+1)
	basics := make(map[string][]model.BasicDaily, len(symbols))
	for i := range symbols {
		symbol := fmt.Sprintf("sh%06d", i)
		symbols[i] = symbol
		basics[symbol] = []model.BasicDaily{{
			Symbol: symbol, Date: day, Close: 10, PreClose: 10,
		}}
	}
	repo := &fakeCalcRepo{basics: basics}
	path := filepath.Join(t.TempDir(), "factor.csv")

	count, err := ExportFactorsToCSV(context.Background(), repo, path, FactorExportOptions{
		FullRecalc: true,
		Symbols:    symbols,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != len(symbols) {
		t.Fatalf("count = %d, want %d", count, len(symbols))
	}
	if len(repo.basicBatchCalls) != 2 {
		t.Fatalf("batch calls = %d, want 2", len(repo.basicBatchCalls))
	}
	if len(repo.basicBatchCalls[0]) != calcQueryBatchSize || len(repo.basicBatchCalls[1]) != 1 {
		t.Fatalf("batch sizes = [%d, %d], want [%d, 1]",
			len(repo.basicBatchCalls[0]), len(repo.basicBatchCalls[1]), calcQueryBatchSize)
	}
	if len(repo.fullBasicCalls) != 0 {
		t.Fatalf("unexpected per-symbol basic calls: %v", repo.fullBasicCalls)
	}
}
