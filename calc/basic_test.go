package calc

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func TestCalculatePreClosePriceRejectsInvalidResult(t *testing.T) {
	prevClose := 1.0
	got := calculatePreClosePrice(prevClose, &xdxrInfo{Fenhong: 12.8})
	if got != prevClose {
		t.Fatalf("calculatePreClosePrice() = %v, want fallback %v", got, prevClose)
	}
}

func TestCalculatePreClosePriceAppliesXdxr(t *testing.T) {
	got := calculatePreClosePrice(10, &xdxrInfo{
		Fenhong:     0.5,
		Peigu:       1,
		Peigujia:    5,
		Songzhuangu: 2,
		Splitc3:     2,
	})
	want := ((10*10 - 0.5) + 1*5) / (10 + 1 + 2) / 2
	if got != want {
		t.Fatalf("calculatePreClosePrice() = %v, want %v", got, want)
	}
}

func TestExportBasicDailyToCSVFiltersSymbols(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	repo := &fakeCalcRepo{
		symbols: []string{"sh600000", "sz000001"},
		klines: map[string][]model.KlineDay{
			"sh600000": {{Symbol: "sh600000", Date: day, Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100}},
			"sz000001": {{Symbol: "sz000001", Date: day, Open: 20, High: 21, Low: 19, Close: 20.5, Volume: 200}},
		},
	}
	path := filepath.Join(t.TempDir(), "basic.csv")

	count, err := ExportBasicDailyToCSV(context.Background(), repo, path, BasicExportOptions{
		Symbols: []string{"sh600000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	rows := readCSVFile(t, path)
	if len(rows) != 2 {
		t.Fatalf("csv rows = %d, want header + 1", len(rows))
	}
	if rows[1][1] != "sh600000" {
		t.Fatalf("symbol = %s, want sh600000", rows[1][1])
	}
}

func TestExportBasicDailyDefaultsIncludeBStock(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	repo := &fakeCalcRepo{
		symbols: []string{"sh900901"},
		klines: map[string][]model.KlineDay{
			"sh900901": {{Symbol: "sh900901", Date: day, Open: 1, High: 1.1, Low: 0.9, Close: 1.05, Volume: 100}},
		},
	}
	path := filepath.Join(t.TempDir(), "basic.csv")

	count, err := ExportBasicDailyToCSV(context.Background(), repo, path, BasicExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	wantClasses := []string{model.ClassStock, model.ClassETF, model.ClassBStock}
	if len(repo.classCalls) != 1 || !reflect.DeepEqual(repo.classCalls[0], wantClasses) {
		t.Fatalf("class calls = %v, want %v", repo.classCalls, wantClasses)
	}
}

func TestCalculateBasicDailyIncrementalMatchesFull(t *testing.T) {
	day1 := date(2026, 1, 2)
	day2 := date(2026, 1, 5)
	klines := []model.KlineDay{
		{Symbol: "sh600000", Date: day1, Open: 10, High: 10.5, Low: 9.5, Close: 10, Volume: 100},
		{Symbol: "sh600000", Date: day2, Open: 11, High: 11.5, Low: 10.5, Close: 11, Volume: 110},
	}

	tests := []struct {
		name  string
		gbbq  []model.GbbqData
		event time.Time
	}{
		{
			name:  "xdxr",
			event: day2,
			gbbq:  []model.GbbqData{{Symbol: "sh600000", Date: day2, Category: 1, C1: 0.5}},
		},
		{
			name:  "etf split",
			event: day2,
			gbbq:  []model.GbbqData{{Symbol: "sh510300", Date: day2, Category: 11, C3: 2}},
		},
		{
			name:  "share change",
			event: day2,
			gbbq:  []model.GbbqData{{Symbol: "sh600000", Date: day2, Category: 2, C1: 100, C2: 200, C3: 150, C4: 300}},
		},
		{
			name:  "non trading event maps to next day",
			event: day1.AddDate(0, 0, 1),
			gbbq:  []model.GbbqData{{Symbol: "sh600000", Date: day1.AddDate(0, 0, 1), Category: 1, C1: 0.5}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			symbol := "sh600000"
			if len(tt.gbbq) > 0 {
				symbol = tt.gbbq[0].Symbol
			}
			rows := append([]model.KlineDay(nil), klines...)
			for i := range rows {
				rows[i].Symbol = symbol
			}
			gbbq := append([]model.GbbqData(nil), tt.gbbq...)
			if len(gbbq) > 0 {
				gbbq[0].Date = tt.event
			}

			full, err := CalculateBasicDaily(rows, gbbq)
			if err != nil {
				t.Fatal(err)
			}
			incremental, err := calculateBasicDailyIncremental(*full[0], rows[1:], gbbq)
			if err != nil {
				t.Fatal(err)
			}

			got := make([]model.BasicDaily, len(incremental))
			for i, row := range incremental {
				got[i] = *row
			}
			want := make([]model.BasicDaily, len(full)-1)
			for i, row := range full[1:] {
				want[i] = *row
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("incremental basic = %+v, want %+v", got, want)
			}
		})
	}
}

func readCSVFile(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
