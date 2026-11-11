package cmd

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

type fakeValidationRepo struct {
	schema               string
	schemaErr            error
	latestDate           time.Time
	symbols              []string
	basicDates           map[string]time.Time
	factorDates          map[string]time.Time
	missingPresenceTable string
	ohlcRows             []ohlcValidationRow
	classCalls           [][]string
}

func (f *fakeValidationRepo) ReadSchemaVersion() (string, error) {
	return f.schema, f.schemaErr
}

func (f *fakeValidationRepo) QueryRows(ctx context.Context, query string, dest interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.Contains(query, "check:ohlc_anomalies") {
		rows := dest.(*[]ohlcValidationRow)
		*rows = append(*rows, f.ohlcRows...)
		return nil
	}
	if strings.Contains(query, "check:data_presence") {
		rows := dest.(*[]validationRow)
		count := int64(1)
		if f.missingPresenceTable != "" && strings.Contains(query, f.missingPresenceTable) {
			count = 0
		}
		*rows = []validationRow{{RowCount: count}}
		return nil
	}
	return nil
}

func (f *fakeValidationRepo) GetLatestDate(tableName string, dateCol string) (time.Time, error) {
	return f.latestDate, nil
}

func (f *fakeValidationRepo) GetLatestDates(tableName string, dateCol string) (map[string]time.Time, error) {
	if tableName == model.TableBasicDaily.TableName {
		return f.basicDates, nil
	}
	return f.factorDates, nil
}

func (f *fakeValidationRepo) GetSymbolsByClass(classes ...string) ([]string, error) {
	f.classCalls = append(f.classCalls, slices.Clone(classes))
	return f.symbols, nil
}

func TestRunValidationWarningOnlyPassesUnlessStrict(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	repo := &fakeValidationRepo{
		schema:               "8.0",
		latestDate:           day,
		symbols:              []string{"sh600000"},
		basicDates:           map[string]time.Time{"sh600000": day},
		factorDates:          map[string]time.Time{"sh600000": day},
		missingPresenceTable: model.TableSymbolName.TableName,
	}

	report, err := runValidation(context.Background(), repo, ValidationOptions{MaxSamples: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed {
		t.Fatal("report.Passed = false, want true for warning-only default mode")
	}
	if report.WarningChecks != 1 {
		t.Fatalf("warning checks = %d, want 1", report.WarningChecks)
	}

	strictReport, err := runValidation(context.Background(), repo, ValidationOptions{
		Strict:     true,
		MaxSamples: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strictReport.Passed {
		t.Fatal("strict report.Passed = true, want false for warning")
	}
}

func TestOHLCHistoricalIndexIsWarningAndStockIsCritical(t *testing.T) {
	day := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	repo := &fakeValidationRepo{
		schema:     "8.0",
		latestDate: latest,
		ohlcRows: []ohlcValidationRow{{
			Symbol: "sh000001", Date: day, Open: 10, High: 9, Low: 10, Close: 9.5,
			Class: model.ClassIndex, HighLTLow: 1,
		}},
	}

	checks, err := checkOHLC(context.Background(), repo, 5)
	if err != nil {
		t.Fatal(err)
	}
	highLow := findValidationCheck(t, checks, "ohlc_high_lt_low")
	if highLow.Severity != SeverityWarning {
		t.Fatalf("historical index severity = %s, want warning", highLow.Severity)
	}

	repo.ohlcRows[0].Symbol = "sh600000"
	repo.ohlcRows[0].Class = model.ClassStock
	checks, err = checkOHLC(context.Background(), repo, 5)
	if err != nil {
		t.Fatal(err)
	}
	highLow = findValidationCheck(t, checks, "ohlc_high_lt_low")
	if highLow.Severity != SeverityCritical {
		t.Fatalf("stock severity = %s, want critical", highLow.Severity)
	}

	repo.ohlcRows[0].Symbol = "sh900901"
	repo.ohlcRows[0].Class = model.ClassBStock
	checks, err = checkOHLC(context.Background(), repo, 5)
	if err != nil {
		t.Fatal(err)
	}
	highLow = findValidationCheck(t, checks, "ohlc_high_lt_low")
	if highLow.Severity != SeverityWarning {
		t.Fatalf("historical bstock severity = %s, want warning", highLow.Severity)
	}
}

func TestLatestDateConsistencyIncludesBStock(t *testing.T) {
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	repo := &fakeValidationRepo{
		symbols:     []string{"sh600000", "sh900901"},
		basicDates:  map[string]time.Time{"sh600000": day},
		factorDates: map[string]time.Time{"sh600000": day},
	}

	check, err := checkLatestDateConsistency(context.Background(), repo, 5)
	if err != nil {
		t.Fatal(err)
	}
	if check.Severity != SeverityCritical || check.Count != 1 {
		t.Fatalf("check = severity %s count %d, want critical count 1", check.Severity, check.Count)
	}
	wantClasses := []string{model.ClassStock, model.ClassETF, model.ClassBStock}
	if len(repo.classCalls) != 1 || !slices.Equal(repo.classCalls[0], wantClasses) {
		t.Fatalf("class calls = %v, want %v", repo.classCalls, wantClasses)
	}
}

func TestValidationJSONIsStable(t *testing.T) {
	report := ValidationReport{
		SchemaVersion:         "8.0",
		ExpectedSchemaVersion: "8.0",
		Passed:                true,
		Checks: []ValidationCheck{{
			Check:    "sample",
			Severity: SeverityWarning,
			Passed:   true,
			Samples: []ValidationSample{{
				Symbol: "sh600000",
				Details: map[string]string{
					"z": "2",
					"a": "1",
				},
			}},
		}},
	}

	var first bytes.Buffer
	var second bytes.Buffer
	if err := writeValidationJSON(&first, report); err != nil {
		t.Fatal(err)
	}
	if err := writeValidationJSON(&second, report); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("JSON output is not stable:\n%s\n%s", first.String(), second.String())
	}
}

func findValidationCheck(t *testing.T, checks []ValidationCheck, name string) ValidationCheck {
	t.Helper()
	for _, check := range checks {
		if check.Check == name {
			return check
		}
	}
	t.Fatalf("check %s not found", name)
	return ValidationCheck{}
}
