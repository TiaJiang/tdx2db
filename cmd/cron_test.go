package cmd

import (
	"context"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/jing2uo/tdx2db/database/duckdb"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/utils"
)

func TestCronHelpHasNoBoardDataDir(t *testing.T) {
	binary := buildCronTestBinary(t)
	output, err := exec.Command(binary, "cron", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("cron --help failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "--board-data-dir") ||
		strings.Contains(string(output), "TDX2DB_BOARD_DATA_DIR") ||
		strings.Contains(string(output), "T0002/hq_cache") {
		t.Fatalf("removed board directory flag remains: %s", output)
	}
}

func TestCronRunsWithoutBoardDataDir(t *testing.T) {
	fixtureDir := t.TempDir()
	today := time.Now().Truncate(24 * time.Hour)
	for today.Weekday() == time.Saturday || today.Weekday() == time.Sunday {
		today = today.AddDate(0, 0, -1)
	}
	dbPath := filepath.Join(t.TempDir(), "cron.duckdb")
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
	if err := driver.WriteSchemaVersion(); err != nil {
		t.Fatal(err)
	}
	holidaysPath := filepath.Join(fixtureDir, "holidays.csv")
	writer, err := utils.NewCSVWriter[model.Holiday](holidaysPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write([]model.Holiday{{Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := driver.ImportHolidays(holidaysPath); err != nil {
		t.Fatal(err)
	}
	klinePath := filepath.Join(fixtureDir, "kline.csv")
	writeCronFixtureCSV(t, klinePath, []model.KlineDay{{
		Symbol: "sh600000", Date: today, Open: 10, High: 10, Low: 10, Close: 10, Volume: 100,
	}})
	if err := driver.ImportKlineDaily(klinePath); err != nil {
		t.Fatal(err)
	}
	if err := driver.RebuildSymbolClass(); err != nil {
		t.Fatal(err)
	}
	basicPath := filepath.Join(fixtureDir, "basic.csv")
	writeCronFixtureCSV(t, basicPath, []model.BasicDaily{{
		Symbol: "sh600000", Date: today, Close: 10, PreClose: 10,
	}})
	if err := driver.ImportBasic(basicPath, nil); err != nil {
		t.Fatal(err)
	}
	factorPath := filepath.Join(fixtureDir, "factor.csv")
	writeCronFixtureCSV(t, factorPath, []model.Factor{{
		Symbol: "sh600000", Date: today, HfqFactor: 1,
	}})
	if err := driver.ImportAdjustFactors(factorPath, nil); err != nil {
		t.Fatal(err)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Cron(context.Background(), "duckdb://"+filepath.ToSlash(dbPath), false); err != nil {
		t.Fatalf("Cron without board data dir failed: %v", err)
	}
}

func writeCronFixtureCSV[T any](t *testing.T, path string, rows []T) {
	t.Helper()
	writer, err := utils.NewCSVWriter[T](path)
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

func buildCronTestBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "tdx2db-test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = ".."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build CLI for help test: %v\n%s", err, output)
	}
	return binary
}
