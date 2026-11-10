package workflow

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/database/duckdb"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

func TestGBBQChangedSymbolsRebuildOnlyAffectedSymbol(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gbbq-selective.duckdb")
	driver, err := duckdb.NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)

	klineCSV := filepath.Join(t.TempDir(), "kline.csv")
	writeKlineCSV(t, klineCSV, []model.KlineDay{
		{Symbol: "sh600000", Date: day1, Open: 9.8, High: 10.1, Low: 9.7, Close: 10, Volume: 100},
		{Symbol: "sh600000", Date: day2, Open: 10.2, High: 11.2, Low: 10.1, Close: 11, Volume: 110},
		{Symbol: "sh600000", Date: day3, Open: 11.1, High: 12.1, Low: 11, Close: 12, Volume: 120},
		{Symbol: "sz000001", Date: day1, Open: 19.8, High: 20.1, Low: 19.7, Close: 20, Volume: 200},
		{Symbol: "sz000001", Date: day2, Open: 20.2, High: 21.2, Low: 20.1, Close: 21, Volume: 210},
		{Symbol: "sz000001", Date: day3, Open: 21.1, High: 22.1, Low: 21, Close: 22, Volume: 220},
	})
	if err := driver.ImportKlineDaily(klineCSV); err != nil {
		t.Fatal(err)
	}

	oldGbbq := []model.GbbqData{
		{Symbol: "sh600000", Date: day2, Category: 1, C1: 0.4},
		{Symbol: "sz000001", Date: day2, Category: 1, C1: 0.2},
	}
	newGbbq := []model.GbbqData{
		{Symbol: "sh600000", Date: day2, Category: 1, C1: 0.5},
		{Symbol: "sz000001", Date: day2, Category: 1, C1: 0.2},
	}
	changed, changedSymbols := diffGbbq(oldGbbq, newGbbq)
	if !changed || len(changedSymbols) != 1 || changedSymbols[0] != "sh600000" {
		t.Fatalf("diffGbbq() = (%t, %v), want (true, [sh600000])", changed, changedSymbols)
	}

	gbbqCSV := filepath.Join(t.TempDir(), "gbbq.csv")
	writeGbbqCSV(t, gbbqCSV, newGbbq)
	if err := driver.ImportGBBQ(gbbqCSV); err != nil {
		t.Fatal(err)
	}
	if err := driver.RebuildSymbolClass(); err != nil {
		t.Fatal(err)
	}

	basicCSV := filepath.Join(t.TempDir(), "basic-sentinel.csv")
	writeBasicCSV(t, basicCSV, []model.BasicDaily{
		{Symbol: "sh600000", Date: day1, Close: 10, PreClose: 999},
		{Symbol: "sh600000", Date: day2, Close: 11, PreClose: 999},
		{Symbol: "sh600000", Date: day3, Close: 12, PreClose: 999},
		{Symbol: "sz000001", Date: day1, Close: 20, PreClose: 999},
		{Symbol: "sz000001", Date: day2, Close: 21, PreClose: 999},
		{Symbol: "sz000001", Date: day3, Close: 22, PreClose: 999},
	})
	if err := driver.ImportBasic(basicCSV, nil); err != nil {
		t.Fatal(err)
	}

	factorCSV := filepath.Join(t.TempDir(), "factor-sentinel.csv")
	writeFactorCSVForWorkflow(t, factorCSV, []model.Factor{
		{Symbol: "sh600000", Date: day1, HfqFactor: 999},
		{Symbol: "sh600000", Date: day2, HfqFactor: 999},
		{Symbol: "sh600000", Date: day3, HfqFactor: 999},
		{Symbol: "sz000001", Date: day1, HfqFactor: 999},
		{Symbol: "sz000001", Date: day2, HfqFactor: 999},
		{Symbol: "sz000001", Date: day3, HfqFactor: 999},
	})
	if err := driver.ImportAdjustFactors(factorCSV, nil); err != nil {
		t.Fatal(err)
	}

	args := &TaskArgs{
		TempDir: t.TempDir(),
		Extra: map[string]interface{}{
			ExtraGbbqChanged:        true,
			ExtraGbbqChangedSymbols: changedSymbols,
		},
	}

	basicResult, err := executeCalcBasic(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if basicResult.State != StateCompleted || basicResult.Rows != 3 {
		t.Fatalf("basic result = state %s rows %d, want completed rows 3", basicResult.State, basicResult.Rows)
	}

	shBasics, err := driver.GetBasicsBySymbol("sh600000")
	if err != nil {
		t.Fatal(err)
	}
	if got := findBasicPreClose(t, shBasics, day2); got == 999 {
		t.Fatal("sh600000 basic was not rebuilt")
	}

	szBasics, err := driver.GetBasicsBySymbol("sz000001")
	if err != nil {
		t.Fatal(err)
	}
	for _, basic := range szBasics {
		if basic.PreClose != 999 {
			t.Fatalf("sz000001 basic was unexpectedly rebuilt: date=%s preclose=%v",
				basic.Date.Format("2006-01-02"), basic.PreClose)
		}
	}

	factorResult, err := executeCalcFactor(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if factorResult.State != StateCompleted || factorResult.Rows != 3 {
		t.Fatalf("factor result = state %s rows %d, want completed rows 3", factorResult.State, factorResult.Rows)
	}

	shFactor, err := driver.GetLatestFactor("sh600000")
	if err != nil {
		t.Fatal(err)
	}
	if shFactor == nil || shFactor.HfqFactor == 999 {
		t.Fatalf("sh600000 factor was not rebuilt: %+v", shFactor)
	}

	szFactor, err := driver.GetLatestFactor("sz000001")
	if err != nil {
		t.Fatal(err)
	}
	if szFactor == nil || szFactor.HfqFactor != 999 {
		t.Fatalf("sz000001 factor was unexpectedly rebuilt: %+v", szFactor)
	}
}

func TestDailyIncrementAppendsWithoutRewritingHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "daily-increment.duckdb")
	driver, err := duckdb.NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)

	klineCSV := filepath.Join(t.TempDir(), "kline.csv")
	writeKlineCSV(t, klineCSV, []model.KlineDay{
		{Symbol: "sh600000", Date: day1, Open: 9.8, High: 10.1, Low: 9.7, Close: 10, Volume: 100},
		{Symbol: "sh600000", Date: day2, Open: 10.2, High: 11.2, Low: 10.1, Close: 11, Volume: 110},
		{Symbol: "sh600000", Date: day3, Open: 11.1, High: 12.1, Low: 11, Close: 12, Volume: 120},
		{Symbol: "sz000001", Date: day1, Open: 19.8, High: 20.1, Low: 19.7, Close: 20, Volume: 200},
		{Symbol: "sz000001", Date: day2, Open: 20.2, High: 21.2, Low: 20.1, Close: 21, Volume: 210},
		{Symbol: "sz000001", Date: day3, Open: 21.1, High: 22.1, Low: 21, Close: 22, Volume: 220},
	})
	if err := driver.ImportKlineDaily(klineCSV); err != nil {
		t.Fatal(err)
	}
	if err := driver.RebuildSymbolClass(); err != nil {
		t.Fatal(err)
	}

	basicCSV := filepath.Join(t.TempDir(), "basic-sentinel.csv")
	writeBasicCSV(t, basicCSV, []model.BasicDaily{
		{Symbol: "sh600000", Date: day1, Close: 10, PreClose: 999},
		{Symbol: "sh600000", Date: day2, Close: 11, PreClose: 999},
		{Symbol: "sz000001", Date: day1, Close: 20, PreClose: 999},
		{Symbol: "sz000001", Date: day2, Close: 21, PreClose: 999},
	})
	if err := driver.ImportBasic(basicCSV, nil); err != nil {
		t.Fatal(err)
	}

	factorCSV := filepath.Join(t.TempDir(), "factor-sentinel.csv")
	writeFactorCSVForWorkflow(t, factorCSV, []model.Factor{
		{Symbol: "sh600000", Date: day1, HfqFactor: 999},
		{Symbol: "sh600000", Date: day2, HfqFactor: 999},
		{Symbol: "sz000001", Date: day1, HfqFactor: 999},
		{Symbol: "sz000001", Date: day2, HfqFactor: 999},
	})
	if err := driver.ImportAdjustFactors(factorCSV, nil); err != nil {
		t.Fatal(err)
	}

	args := &TaskArgs{
		TempDir: t.TempDir(),
		Extra: map[string]interface{}{
			ExtraGbbqChanged:        false,
			ExtraGbbqChangedSymbols: []string(nil),
		},
	}

	basicResult, err := executeCalcBasic(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if basicResult.State != StateCompleted || basicResult.Rows != 2 {
		t.Fatalf("basic result = state %s rows %d, want completed rows 2", basicResult.State, basicResult.Rows)
	}
	for _, symbol := range []string{"sh600000", "sz000001"} {
		basics, err := driver.GetBasicsBySymbol(symbol)
		if err != nil {
			t.Fatal(err)
		}
		if len(basics) != 3 {
			t.Fatalf("%s basic rows = %d, want 3", symbol, len(basics))
		}
		if basics[0].PreClose != 999 || basics[1].PreClose != 999 {
			t.Fatalf("%s historical basics were rewritten: %+v", symbol, basics[:2])
		}
		if basics[2].Date.Format("2006-01-02") != day3.Format("2006-01-02") {
			t.Fatalf("%s latest basic date = %s, want %s", symbol, basics[2].Date, day3)
		}
	}

	factorResult, err := executeCalcFactor(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if factorResult.State != StateCompleted || factorResult.Rows != 2 {
		t.Fatalf("factor result = state %s rows %d, want completed rows 2", factorResult.State, factorResult.Rows)
	}
	for _, symbol := range []string{"sh600000", "sz000001"} {
		basics, err := driver.GetBasicsBySymbol(symbol)
		if err != nil {
			t.Fatal(err)
		}
		factor, err := driver.GetLatestFactor(symbol)
		if err != nil {
			t.Fatal(err)
		}
		if factor == nil || !factor.Date.Equal(basics[len(basics)-1].Date) || factor.HfqFactor != 999 {
			t.Fatalf("%s factor = %+v, want date %s factor 999", symbol, factor, day3)
		}
	}

	basicResult, err = executeCalcBasic(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if basicResult.State != StateSkipped {
		t.Fatalf("second basic state = %s, want skipped", basicResult.State)
	}
	factorResult, err = executeCalcFactor(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if factorResult.State != StateSkipped {
		t.Fatalf("second factor state = %s, want skipped", factorResult.State)
	}
}

func TestBStockIncludedInBasicAndFactor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bstock.duckdb")
	driver, err := duckdb.NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	klineCSV := filepath.Join(t.TempDir(), "kline.csv")
	writeKlineCSV(t, klineCSV, []model.KlineDay{
		{Symbol: "sh900901", Date: day1, Open: 1, High: 1.1, Low: 0.9, Close: 1.05, Volume: 100},
		{Symbol: "sh900901", Date: day2, Open: 1.06, High: 1.2, Low: 1.01, Close: 1.15, Volume: 120},
	})
	if err := driver.ImportKlineDaily(klineCSV); err != nil {
		t.Fatal(err)
	}
	if err := driver.RebuildSymbolClass(); err != nil {
		t.Fatal(err)
	}

	args := &TaskArgs{
		TempDir: t.TempDir(),
		Extra: map[string]interface{}{
			ExtraGbbqChanged:        false,
			ExtraGbbqChangedSymbols: []string(nil),
		},
	}

	basicResult, err := executeCalcBasic(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if basicResult.State != StateCompleted || basicResult.Rows != 2 {
		t.Fatalf("basic result = state %s rows %d, want completed rows 2", basicResult.State, basicResult.Rows)
	}

	factorResult, err := executeCalcFactor(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if factorResult.State != StateCompleted || factorResult.Rows != 2 {
		t.Fatalf("factor result = state %s rows %d, want completed rows 2", factorResult.State, factorResult.Rows)
	}

	basics, err := driver.GetBasicsBySymbol("sh900901")
	if err != nil {
		t.Fatal(err)
	}
	if len(basics) != 2 {
		t.Fatalf("bstock basic rows = %d, want 2", len(basics))
	}
	var factors []model.Factor
	if err := driver.QueryRows(context.Background(),
		"SELECT * FROM "+model.TableAdjustFactor.TableName+" WHERE symbol = 'sh900901' ORDER BY date",
		&factors,
	); err != nil {
		t.Fatal(err)
	}
	if len(factors) != 2 {
		t.Fatalf("bstock factor rows = %d, want 2", len(factors))
	}

	basicResult, err = executeCalcBasic(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if basicResult.State != StateSkipped {
		t.Fatalf("second basic state = %s, want skipped", basicResult.State)
	}
	factorResult, err = executeCalcFactor(context.Background(), driver, args)
	if err != nil {
		t.Fatal(err)
	}
	if factorResult.State != StateSkipped {
		t.Fatalf("second factor state = %s, want skipped", factorResult.State)
	}
}

func writeKlineCSV(t *testing.T, path string, rows []model.KlineDay) {
	t.Helper()
	writer, err := utils.NewCSVWriter[model.KlineDay](path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(rows); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeGbbqCSV(t *testing.T, path string, rows []model.GbbqData) {
	t.Helper()
	writer, err := utils.NewCSVWriter[model.GbbqData](path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(rows); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeBasicCSV(t *testing.T, path string, rows []model.BasicDaily) {
	t.Helper()
	writer, err := utils.NewCSVWriter[model.BasicDaily](path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(rows); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeFactorCSVForWorkflow(t *testing.T, path string, rows []model.Factor) {
	t.Helper()
	writer, err := utils.NewCSVWriter[model.Factor](path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(rows); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func findBasicPreClose(t *testing.T, basics []model.BasicDaily, date time.Time) float64 {
	t.Helper()
	for _, basic := range basics {
		if basic.Date.Format("2006-01-02") == date.Format("2006-01-02") {
			return basic.PreClose
		}
	}
	t.Fatalf("basic date %s not found", date.Format("2006-01-02"))
	return 0
}
