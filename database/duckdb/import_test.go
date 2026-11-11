package duckdb

import (
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

func TestImportBasicUpsertsBySymbolAndDate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")
	driver, err := NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
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

	csvPath := filepath.Join(t.TempDir(), "basic.csv")
	writer, err := utils.NewCSVWriter[model.BasicDaily](csvPath)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	if err := writer.Write([]model.BasicDaily{
		{Symbol: "sh600000", Date: day, Close: 10, PreClose: 9.8},
		{Symbol: "sh600000", Date: day.AddDate(0, 0, 1), Close: 10.2, PreClose: 10},
	}); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	if err := driver.ImportBasic(csvPath, nil); err != nil {
		t.Fatal(err)
	}
	if err := driver.ImportBasic(csvPath, nil); err != nil {
		t.Fatal(err)
	}

	var count int64
	if err := driver.db.Get(&count, "SELECT COUNT(*) FROM "+model.TableBasicDaily.TableName); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("row count = %d, want 2", count)
	}

	var duplicates int64
	query := `
		SELECT COUNT(*) FROM (
			SELECT symbol, date, COUNT(*) AS row_count
			FROM ` + model.TableBasicDaily.TableName + `
			GROUP BY symbol, date
			HAVING COUNT(*) > 1
		) duplicates
	`
	if err := driver.db.Get(&duplicates, query); err != nil {
		t.Fatal(err)
	}
	if duplicates != 0 {
		t.Fatalf("duplicate keys = %d, want 0", duplicates)
	}

	incrementalPath := filepath.Join(t.TempDir(), "basic-incremental.csv")
	writer, err = utils.NewCSVWriter[model.BasicDaily](incrementalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write([]model.BasicDaily{
		{Symbol: "sh600000", Date: day.AddDate(0, 0, 1), Close: 10.3, PreClose: 10},
	}); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := driver.ImportBasic(incrementalPath, nil); err != nil {
		t.Fatal(err)
	}

	var firstClose float64
	if err := driver.db.Get(&firstClose,
		"SELECT close FROM "+model.TableBasicDaily.TableName+
			" WHERE symbol = 'sh600000' AND date = ?", day); err != nil {
		t.Fatal(err)
	}
	if firstClose != 10 {
		t.Fatalf("historical close = %v, want preserved 10", firstClose)
	}
	if err := driver.db.Get(&count, "SELECT COUNT(*) FROM "+model.TableBasicDaily.TableName); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("row count after incremental import = %d, want 2", count)
	}
}

func TestImportAdjustFactorsReplacesSelectedSymbolsOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")
	driver, err := NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
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
	initialPath := filepath.Join(t.TempDir(), "factor-initial.csv")
	writeFactorCSV(t, initialPath, []model.Factor{
		{Symbol: "sh600000", Date: day1, HfqFactor: 1},
		{Symbol: "sh600000", Date: day2, HfqFactor: 2},
		{Symbol: "sz000001", Date: day1, HfqFactor: 3},
		{Symbol: "sz000001", Date: day2, HfqFactor: 4},
	})
	if err := driver.ImportAdjustFactors(initialPath, nil); err != nil {
		t.Fatal(err)
	}

	selectivePath := filepath.Join(t.TempDir(), "factor-selective.csv")
	writeFactorCSV(t, selectivePath, []model.Factor{
		{Symbol: "sh600000", Date: day1, HfqFactor: 10},
		{Symbol: "sh600000", Date: day2, HfqFactor: 20},
		{Symbol: "sz000001", Date: day2, HfqFactor: 40},
	})
	if err := driver.ImportAdjustFactors(selectivePath, []string{"sh600000"}); err != nil {
		t.Fatal(err)
	}
	if err := driver.ImportAdjustFactors(selectivePath, []string{"sh600000"}); err != nil {
		t.Fatal(err)
	}

	var count int64
	if err := driver.db.Get(&count, "SELECT COUNT(*) FROM "+model.TableAdjustFactor.TableName); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("factor row count = %d, want 4", count)
	}

	var shFactor float64
	if err := driver.db.Get(&shFactor,
		"SELECT hfq_factor FROM "+model.TableAdjustFactor.TableName+
			" WHERE symbol = 'sh600000' AND date = ?", day1); err != nil {
		t.Fatal(err)
	}
	if shFactor != 10 {
		t.Fatalf("sh600000 hfq = %v, want 10", shFactor)
	}

	var szFactor float64
	if err := driver.db.Get(&szFactor,
		"SELECT hfq_factor FROM "+model.TableAdjustFactor.TableName+
			" WHERE symbol = 'sz000001' AND date = ?", day1); err != nil {
		t.Fatal(err)
	}
	if szFactor != 3 {
		t.Fatalf("sz000001 historical hfq = %v, want preserved 3", szFactor)
	}
}

func writeFactorCSV(t *testing.T, path string, rows []model.Factor) {
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

func TestImportBlockSnapshotReplacesBothTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")
	driver, err := NewDuckDBDriver(&url.URL{Scheme: "duckdb", Path: dbPath})
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

	oldInfo, oldMembers := writeBlockCSVs(t, []model.BlockInfo{
		{BlockType: "concept", BlockName: "old", BlockCode: "sh880001", BlockSymbol: "sh880001", BlockLevel: 1},
	}, []model.BlockMember{
		{StockSymbol: "sz000001", BlockCode: "sh880001"},
	})
	if err := driver.ImportBlockSnapshot(oldInfo, oldMembers); err != nil {
		t.Fatal(err)
	}

	newInfo, newMembers := writeBlockCSVs(t, []model.BlockInfo{
		{BlockType: "index", BlockName: "new", BlockCode: "sh000300", BlockSymbol: "sh000300", BlockLevel: 1},
	}, []model.BlockMember{
		{StockSymbol: "sh600000", BlockCode: "sh000300"},
	})
	if err := driver.ImportBlockSnapshot(newInfo, newMembers); err != nil {
		t.Fatal(err)
	}
	if err := driver.ImportBlockSnapshot(newInfo, newMembers); err != nil {
		t.Fatal(err)
	}

	var infoCount, memberCount int64
	if err := driver.db.Get(&infoCount, "SELECT COUNT(*) FROM "+model.TableBlockInfo.TableName); err != nil {
		t.Fatal(err)
	}
	if err := driver.db.Get(&memberCount, "SELECT COUNT(*) FROM "+model.TableBlockMember.TableName); err != nil {
		t.Fatal(err)
	}
	if infoCount != 1 || memberCount != 1 {
		t.Fatalf("counts = (%d, %d), want (1, 1)", infoCount, memberCount)
	}
	var nullParents int64
	if err := driver.db.Get(&nullParents,
		"SELECT COUNT(*) FROM "+model.TableBlockInfo.TableName+" WHERE parent_code IS NULL"); err != nil {
		t.Fatal(err)
	}
	if nullParents != 0 {
		t.Fatalf("null parent_code rows = %d, want 0", nullParents)
	}

	badMemberPath := filepath.Join(t.TempDir(), "missing-member.csv")
	if err := driver.ImportBlockSnapshot(newInfo, badMemberPath); err == nil {
		t.Fatal("expected missing member CSV to fail")
	}
	if err := driver.db.Get(&infoCount, "SELECT COUNT(*) FROM "+model.TableBlockInfo.TableName); err != nil {
		t.Fatal(err)
	}
	if err := driver.db.Get(&memberCount, "SELECT COUNT(*) FROM "+model.TableBlockMember.TableName); err != nil {
		t.Fatal(err)
	}
	if infoCount != 1 || memberCount != 1 {
		t.Fatalf("counts after failed import = (%d, %d), want (1, 1)", infoCount, memberCount)
	}
}

func writeBlockCSVs(t *testing.T, infos []model.BlockInfo, members []model.BlockMember) (string, string) {
	t.Helper()
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "blocks-info.csv")
	infoWriter, err := utils.NewCSVWriter[model.BlockInfo](infoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := infoWriter.Write(infos); err != nil {
		infoWriter.Close()
		t.Fatal(err)
	}
	if err := infoWriter.Close(); err != nil {
		t.Fatal(err)
	}

	memberPath := filepath.Join(dir, "blocks-member.csv")
	memberWriter, err := utils.NewCSVWriter[model.BlockMember](memberPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := memberWriter.Write(members); err != nil {
		memberWriter.Close()
		t.Fatal(err)
	}
	if err := memberWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return infoPath, memberPath
}
