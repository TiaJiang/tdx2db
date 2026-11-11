package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSelectBasicSymbolsSkipsUnchangedSymbols(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	daily := map[string]time.Time{
		"sh600000": day,
		"sz000001": day,
	}
	basic := map[string]time.Time{
		"sh600000": day,
		"sz000001": day,
	}

	symbols := selectBasicSymbols(daily, basic, []string{"sh600000", "sz000001"}, nil, false)
	if len(symbols) != 0 {
		t.Fatalf("selectBasicSymbols() = %v, want empty", symbols)
	}
}

func TestSelectBasicSymbolsIncludesChangedAndLaggingSymbols(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	daily := map[string]time.Time{
		"sh600000": day,
		"sh600001": day,
		"sz000001": day,
	}
	basic := map[string]time.Time{
		"sh600000": day,
		"sh600001": day.AddDate(0, 0, -1),
		"sz000001": day,
	}

	symbols := selectBasicSymbols(
		daily,
		basic,
		[]string{"sh600000", "sh600001", "sz000001"},
		[]string{"sz000001"},
		false,
	)
	want := []string{"sh600001", "sz000001"}
	if !reflect.DeepEqual(symbols, want) {
		t.Fatalf("selectBasicSymbols() = %v, want %v", symbols, want)
	}
}

func TestSelectBasicSymbolsFullRecalcIncludesEligibleOnly(t *testing.T) {
	daily := map[string]time.Time{
		"sh600000": time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}

	symbols := selectBasicSymbols(
		daily,
		nil,
		[]string{"sh600000", "sh000001"},
		nil,
		true,
	)
	want := []string{"sh000001", "sh600000"}
	if !reflect.DeepEqual(symbols, want) {
		t.Fatalf("selectBasicSymbols() = %v, want %v", symbols, want)
	}
}

func TestCanUsePrecomputedFactors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.csv")
	if err := os.WriteFile(path, []byte("symbol,date,hfq_factor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !canUsePrecomputedFactors(path, 2, []string{"sh600000", "sz000001"}, []string{"sz000001", "sh600000"}) {
		t.Fatal("expected matching symbol sets to use precomputed factors")
	}
	if canUsePrecomputedFactors(path, 2, []string{"sh600000"}, []string{"sh600000", "sz000001"}) {
		t.Fatal("partial symbol coverage must not use precomputed factors")
	}
	if canUsePrecomputedFactors(filepath.Join(t.TempDir(), "missing.csv"), 2, []string{"sh600000"}, []string{"sh600000"}) {
		t.Fatal("missing csv must not use precomputed factors")
	}
}
