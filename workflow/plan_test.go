package workflow

import (
	"slices"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

type fakePlanRepo struct {
	database.DataRepository

	holidays      []time.Time
	symbols       []string
	latestDates   map[string]map[string]time.Time
	invalidFactor bool
	classCalls    [][]string
}

func (f *fakePlanRepo) GetHolidays() ([]time.Time, error) {
	return f.holidays, nil
}

func (f *fakePlanRepo) GetLatestDates(tableName string, dateCol string) (map[string]time.Time, error) {
	return f.latestDates[tableName], nil
}

func (f *fakePlanRepo) GetSymbolsByClass(classes ...string) ([]string, error) {
	f.classCalls = append(f.classCalls, slices.Clone(classes))
	return f.symbols, nil
}

func (f *fakePlanRepo) HasInvalidFactors() (bool, error) {
	return f.invalidFactor, nil
}

func planDate(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}

func TestBuildWorkPlanDetectsMissingBStockBasicAndFactor(t *testing.T) {
	today := planDate(2026, 1, 5)
	bDay := planDate(2026, 1, 2)
	repo := &fakePlanRepo{
		holidays: []time.Time{planDate(2026, 1, 1)},
		symbols:  []string{"sh600000", "sh510300", "sh900901"},
		latestDates: map[string]map[string]time.Time{
			model.TableKlineDaily.TableName: {
				"sh600000": today,
				"sh510300": today,
				"sh900901": bDay,
			},
			model.TableBasicDaily.TableName: {
				"sh600000": today,
				"sh510300": today,
			},
			model.TableAdjustFactor.TableName: {
				"sh600000": today,
				"sh510300": today,
			},
		},
	}

	plan, err := BuildWorkPlan(repo, today)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NeedDaily {
		t.Fatal("NeedDaily = true, want false")
	}
	if !plan.NeedBasic {
		t.Fatal("NeedBasic = false, want true for missing bstock basic")
	}
	if !plan.NeedFactor {
		t.Fatal("NeedFactor = false, want true for missing bstock factor")
	}
	wantClasses := []string{model.ClassStock, model.ClassETF, model.ClassBStock}
	if len(repo.classCalls) != 1 || !slices.Equal(repo.classCalls[0], wantClasses) {
		t.Fatalf("class calls = %v, want %v", repo.classCalls, wantClasses)
	}
}

func TestBuildWorkPlanDetectsMissingBStockFactor(t *testing.T) {
	today := planDate(2026, 1, 5)
	repo := &fakePlanRepo{
		holidays: []time.Time{planDate(2026, 1, 1)},
		symbols:  []string{"sh600000", "sh900901"},
		latestDates: map[string]map[string]time.Time{
			model.TableKlineDaily.TableName: {
				"sh600000": today,
				"sh900901": today,
			},
			model.TableBasicDaily.TableName: {
				"sh600000": today,
				"sh900901": today,
			},
			model.TableAdjustFactor.TableName: {
				"sh600000": today,
			},
		},
	}

	plan, err := BuildWorkPlan(repo, today)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NeedBasic {
		t.Fatal("NeedBasic = true, want false")
	}
	if !plan.NeedFactor {
		t.Fatal("NeedFactor = false, want true for missing bstock factor")
	}
}
