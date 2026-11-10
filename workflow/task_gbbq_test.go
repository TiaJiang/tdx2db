package workflow

import (
	"reflect"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func TestEqualGbbqIgnoresOrderAndLocation(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	a := []model.GbbqData{
		{Category: 1, Symbol: "sh600000", Date: day, C1: 0.5},
		{Category: 5, Symbol: "sh600000", Date: day.AddDate(0, 0, 1), C3: 100},
	}
	b := []model.GbbqData{
		{Category: 5, Symbol: "sh600000", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)), C3: 100},
		{Category: 1, Symbol: "sh600000", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)), C1: 0.5},
	}

	if !equalGbbq(a, b) {
		t.Fatal("equalGbbq() = false, want true")
	}
	b[0].C3 = 101
	if equalGbbq(a, b) {
		t.Fatal("equalGbbq() = true, want false")
	}
}

func TestDiffGbbqIdentifiesFieldAddAndDeleteSymbols(t *testing.T) {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	oldData := []model.GbbqData{
		{Symbol: "sh600000", Date: day, Category: 1, C1: 0.5},
		{Symbol: "sh600001", Date: day, Category: 5, C3: 100},
		{Symbol: "sz000001", Date: day, Category: 1, C1: 0.1},
	}
	newData := []model.GbbqData{
		{Symbol: "sh600000", Date: day, Category: 1, C1: 0.6},
		{Symbol: "sh600002", Date: day, Category: 5, C3: 200},
		{Symbol: "sz000001", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)), Category: 1, C1: 0.1},
	}

	changed, symbols := diffGbbq(oldData, newData)
	if !changed {
		t.Fatal("diffGbbq() changed = false, want true")
	}
	want := []string{"sh600000", "sh600001", "sh600002"}
	if !reflect.DeepEqual(symbols, want) {
		t.Fatalf("diffGbbq() symbols = %v, want %v", symbols, want)
	}
}

func TestDiffGbbqNoChangeAcrossTimezone(t *testing.T) {
	a := []model.GbbqData{{
		Symbol: "sh600000", Category: 1, C1: 0.5,
		Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}}
	b := []model.GbbqData{{
		Symbol: "sh600000", Category: 1, C1: 0.5,
		Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)),
	}}

	changed, symbols := diffGbbq(a, b)
	if changed || len(symbols) != 0 {
		t.Fatalf("diffGbbq() = (%t, %v), want (false, empty)", changed, symbols)
	}
}
