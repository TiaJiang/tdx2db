package model_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jing2uo/tdx2db/database/duckdb"
	"github.com/jing2uo/tdx2db/model"
)

func TestAllViewsExcludeBFQViews(t *testing.T) {
	expected := map[string]bool{
		"v_stock_qfq":  true,
		"v_stock_hfq":  true,
		"v_etf_qfq":    true,
		"v_etf_hfq":    true,
		"v_bstock_qfq": true,
		"v_bstock_hfq": true,
		"v_index":      true,
	}

	views := model.AllViews()
	if len(views) != len(expected) {
		t.Fatalf("view count = %d, want %d", len(views), len(expected))
	}
	for _, view := range views {
		if strings.HasSuffix(view.Name, "_bfq") {
			t.Errorf("deprecated BFQ view is still registered: %s", view.Name)
		}
		if !expected[view.Name] {
			t.Errorf("unexpected registered view: %s", view.Name)
		}
	}

	deprecated := model.DeprecatedViewNames()
	for _, name := range []string{"v_stock_bfq", "v_etf_bfq", "v_bstock_bfq", "v_index_bfq"} {
		if !contains(deprecated, name) {
			t.Errorf("deprecated view %s is not registered for cleanup", name)
		}
	}
}

func TestDuckDBQFQViewUsesScalarLatestFactor(t *testing.T) {
	sql := model.ViewStockQFQ.DuckDB
	if !strings.Contains(sql, "SELECT lf.hfq_factor FROM raw_adjust_factor lf") {
		t.Fatal("DuckDB QFQ view does not use scalar latest-factor lookup")
	}
	if strings.Contains(sql, "WITH latest_factors") {
		t.Fatal("DuckDB QFQ view still aggregates all latest factors")
	}
}

func TestClickHouseQFQViewKeepsLatestFactorCTE(t *testing.T) {
	sql := model.ViewStockQFQ.ClickHouse
	if !strings.Contains(sql, "WITH latest_factors") {
		t.Fatal("ClickHouse QFQ view is missing latest-factor CTE")
	}
}

func TestETFViewsExcludeMarketValueColumns(t *testing.T) {
	views := []model.ViewDef{model.ViewETFQFQ, model.ViewETFHFQ}
	for _, view := range views {
		for name, sql := range map[string]string{"duckdb": view.DuckDB, "clickhouse": view.ClickHouse} {
			for _, column := range []string{"b.turnover", "b.floatmv", "b.totalmv"} {
				if strings.Contains(sql, column) {
					t.Errorf("%s %s view contains %s", view.Name, name, column)
				}
			}
			for _, column := range []string{"AS open", "AS preclose", "AS volume", "AS amount", "AS change_pct", "AS amplitude"} {
				if !strings.Contains(sql, column) {
					t.Errorf("%s %s view is missing %s", view.Name, name, column)
				}
			}
		}
	}
}

func TestStockViewsKeepMarketValueColumns(t *testing.T) {
	views := []model.ViewDef{model.ViewStockQFQ, model.ViewStockHFQ}
	for _, view := range views {
		for name, sql := range map[string]string{"duckdb": view.DuckDB, "clickhouse": view.ClickHouse} {
			for _, column := range []string{"b.turnover", "b.floatmv", "b.totalmv"} {
				if !strings.Contains(sql, column) {
					t.Errorf("%s %s view is missing %s", view.Name, name, column)
				}
			}
		}
	}
}

func TestAdjustedViewsKeepPricePrecision(t *testing.T) {
	tests := []struct {
		view      model.ViewDef
		precision int
	}{
		{model.ViewStockQFQ, 2},
		{model.ViewStockHFQ, 2},
		{model.ViewETFQFQ, 3},
		{model.ViewETFHFQ, 3},
		{model.ViewBStockQFQ, 3},
		{model.ViewBStockHFQ, 3},
	}

	for _, test := range tests {
		for name, sql := range map[string]string{"duckdb": test.view.DuckDB, "clickhouse": test.view.ClickHouse} {
			for _, column := range []string{"open", "high", "low", "close", "preclose"} {
				want := fmt.Sprintf(", %d) AS %s", test.precision, column)
				if !strings.Contains(sql, want) {
					t.Errorf("%s %s view does not round %s to %d decimals", test.view.Name, name, column, test.precision)
				}
			}
		}
	}
}

func TestBStockViewsFilterClassAndKeepMarketValueColumns(t *testing.T) {
	for _, view := range []model.ViewDef{model.ViewBStockQFQ, model.ViewBStockHFQ} {
		for name, sql := range map[string]string{"duckdb": view.DuckDB, "clickhouse": view.ClickHouse} {
			if !strings.Contains(sql, "sc.class = 'bstock'") {
				t.Errorf("%s %s view does not filter bstock", view.Name, name)
			}
			for _, column := range []string{"b.turnover", "b.floatmv", "b.totalmv"} {
				if !strings.Contains(sql, column) {
					t.Errorf("%s %s view is missing %s", view.Name, name, column)
				}
			}
		}
	}
}

func TestIndexViewUsesOnlyRawKlineAndExposesIndexColumns(t *testing.T) {
	for name, sql := range map[string]string{"duckdb": model.ViewIndex.DuckDB, "clickhouse": model.ViewIndex.ClickHouse} {
		for _, required := range []string{
			"raw_kline_daily",
			"raw_symbol_class",
			"sc.class = 'index'",
			"AS open",
			"AS high",
			"AS low",
			"AS close",
			"AS volume",
			"AS amount",
			"AS up_count",
			"AS down_count",
		} {
			if !strings.Contains(sql, required) {
				t.Errorf("%s index view is missing %s", name, required)
			}
		}

		for _, forbidden := range []string{
			"raw_basic_daily",
			"raw_adjust_factor",
			"turnover",
			"floatmv",
			"totalmv",
			"hfq_factor",
			"qfq_factor",
		} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("%s index view unexpectedly references %s", name, forbidden)
			}
		}

		for _, column := range []string{"open", "high", "low", "close"} {
			if !strings.Contains(sql, ", 2) AS "+column) {
				t.Errorf("%s index view does not round %s to 2 decimals", name, column)
			}
		}
	}
}

func TestDuckDBInitSchemaCreatesNoBFQViews(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.duckdb")
	driver := newDuckDBDriver(t, dbPath)
	defer closeDriver(t, driver)

	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}

	names := queryViewNames(t, driver)
	for _, name := range model.DeprecatedViewNames() {
		if names[name] {
			t.Errorf("fresh database contains deprecated view %s", name)
		}
	}
	for _, name := range []string{
		"v_stock_qfq", "v_stock_hfq",
		"v_etf_qfq", "v_etf_hfq",
		"v_bstock_qfq", "v_bstock_hfq",
		"v_index",
	} {
		if !names[name] {
			t.Errorf("fresh database is missing view %s", name)
		}
	}
}

func TestDuckDBInitSchemaDropsLegacyBFQViews(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.duckdb")
	rawDB, err := sql.Open("duckdb", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range model.DeprecatedViewNames() {
		if _, err := rawDB.Exec("CREATE VIEW " + name + " AS SELECT 1 AS value"); err != nil {
			rawDB.Close()
			t.Fatalf("create legacy view %s: %v", name, err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}

	driver := newDuckDBDriver(t, dbPath)
	defer closeDriver(t, driver)
	if err := driver.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if err := driver.InitSchema(); err != nil {
		t.Fatalf("second InitSchema should be idempotent: %v", err)
	}

	names := queryViewNames(t, driver)
	for _, name := range model.DeprecatedViewNames() {
		if names[name] {
			t.Errorf("legacy view %s was not dropped", name)
		}
	}
}

func TestDuckDBAdjustedViewsFilterByClass(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "classes.duckdb")
	driver := newDuckDBDriver(t, dbPath)
	if err := driver.InitSchema(); err != nil {
		closeDriver(t, driver)
		t.Fatal(err)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()

	classes := map[string]string{
		"sh600000": model.ClassStock,
		"sh510300": model.ClassETF,
		"sh900901": model.ClassBStock,
		"sh000001": model.ClassIndex,
	}
	for symbol, class := range classes {
		mustExec(t, db, fmt.Sprintf("INSERT INTO %s (symbol, class) VALUES (?, ?)", model.TableSymbolClass.TableName), symbol, class)
		mustExec(t, db, fmt.Sprintf(`
			INSERT INTO %s (symbol, open, high, low, close, amount, volume, up_count, down_count, date)
			VALUES (?, 10, 11, 9, 10.5, 1000, 100, 1, 2, DATE '2026-01-05')
		`, model.TableKlineDaily.TableName), symbol)
		mustExec(t, db, fmt.Sprintf(`
			INSERT INTO %s (date, symbol, close, preclose, change_pct, amplitude, turnover, floatmv, totalmv)
			VALUES (DATE '2026-01-05', ?, 10.5, 10, 5, 20, 1, 2, 3)
		`, model.TableBasicDaily.TableName), symbol)
		mustExec(t, db, fmt.Sprintf(`
			INSERT INTO %s (symbol, date, hfq_factor)
			VALUES (?, DATE '2026-01-05', 2)
		`, model.TableAdjustFactor.TableName), symbol)
	}

	tests := []struct {
		view   string
		symbol string
	}{
		{"v_stock_qfq", "sh600000"},
		{"v_stock_hfq", "sh600000"},
		{"v_etf_qfq", "sh510300"},
		{"v_etf_hfq", "sh510300"},
		{"v_bstock_qfq", "sh900901"},
		{"v_bstock_hfq", "sh900901"},
		{"v_index", "sh000001"},
	}
	for _, test := range tests {
		symbols := queryViewSymbols(t, db, test.view)
		if len(symbols) != 1 || symbols[0] != test.symbol {
			t.Errorf("%s symbols = %v, want [%s]", test.view, symbols, test.symbol)
		}
	}
}

func newDuckDBDriver(t *testing.T, dbPath string) *duckdb.DuckDBDriver {
	t.Helper()
	driver, err := duckdb.NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Connect(); err != nil {
		t.Fatal(err)
	}
	return driver
}

func closeDriver(t *testing.T, driver *duckdb.DuckDBDriver) {
	t.Helper()
	if err := driver.Close(); err != nil {
		t.Errorf("close driver: %v", err)
	}
}

func queryViewNames(t *testing.T, driver *duckdb.DuckDBDriver) map[string]bool {
	t.Helper()
	var rows []struct {
		TableName string `col:"table_name"`
	}
	if err := driver.QueryRows(context.Background(), "SELECT table_name FROM information_schema.views", &rows); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(rows))
	for _, row := range rows {
		names[row.TableName] = true
	}
	return names
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func queryViewSymbols(t *testing.T, db *sql.DB, view string) []string {
	t.Helper()
	rows, err := db.Query("SELECT symbol FROM " + view + " ORDER BY symbol")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var symbol string
		if err := rows.Scan(&symbol); err != nil {
			t.Fatal(err)
		}
		symbols = append(symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return symbols
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
