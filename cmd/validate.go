package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

type ValidationSeverity string

const (
	SeverityOK       ValidationSeverity = "ok"
	SeverityWarning  ValidationSeverity = "warning"
	SeverityCritical ValidationSeverity = "critical"
)

var ErrValidationFailed = errors.New("validation failed")

type ValidationSample struct {
	Symbol  string            `json:"symbol,omitempty"`
	Date    string            `json:"date,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

type ValidationCheck struct {
	Check       string             `json:"check"`
	Severity    ValidationSeverity `json:"severity"`
	Count       int64              `json:"count"`
	SymbolCount int64              `json:"symbol_count"`
	Samples     []ValidationSample `json:"samples,omitempty"`
	Passed      bool               `json:"passed"`
	Message     string             `json:"message,omitempty"`
}

type ValidationReport struct {
	SchemaVersion         string            `json:"schema_version"`
	ExpectedSchemaVersion string            `json:"expected_schema_version"`
	Strict                bool              `json:"strict"`
	Passed                bool              `json:"passed"`
	CriticalChecks        int               `json:"critical_checks"`
	WarningChecks         int               `json:"warning_checks"`
	Checks                []ValidationCheck `json:"checks"`
}

type ValidationOptions struct {
	Strict     bool
	MaxSamples int
}

type validationRepository interface {
	ReadSchemaVersion() (string, error)
	QueryRows(ctx context.Context, query string, dest interface{}) error
	GetLatestDate(tableName string, dateCol string) (time.Time, error)
	GetLatestDates(tableName string, dateCol string) (map[string]time.Time, error)
	GetSymbolsByClass(classes ...string) ([]string, error)
}

type validationRow struct {
	Symbol   string       `col:"symbol"`
	Date     sql.NullTime `col:"date"`
	RowCount int64        `col:"row_count"`
	Value    float64      `col:"value"`
	Detail   string       `col:"detail"`
}

type ohlcValidationRow struct {
	Symbol      string    `col:"symbol"`
	Date        time.Time `col:"date"`
	Open        float64   `col:"open"`
	High        float64   `col:"high"`
	Low         float64   `col:"low"`
	Close       float64   `col:"close"`
	Class       string    `col:"class"`
	NonPositive int       `col:"nonpositive"`
	HighLTLow   int       `col:"high_lt_low"`
	HighLTMax   int       `col:"high_lt_max"`
	LowGTMin    int       `col:"low_gt_min"`
}

func Validate(ctx context.Context, dbURI, format string, strict bool) error {
	if format != "table" && format != "json" {
		return fmt.Errorf("unsupported format %q: must be table or json", format)
	}

	db, err := database.NewDB(dbURI)
	if err != nil {
		return fmt.Errorf("failed to create database driver: %w", err)
	}
	if err := db.Connect(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	report, err := runValidation(ctx, db, ValidationOptions{
		Strict:     strict,
		MaxSamples: 5,
	})
	if err != nil {
		return err
	}

	if format == "json" {
		if err := writeValidationJSON(os.Stdout, report); err != nil {
			return err
		}
	} else {
		if err := writeValidationTable(os.Stdout, report); err != nil {
			return err
		}
	}

	if !report.Passed {
		return fmt.Errorf("%w: %d critical checks, %d warning checks",
			ErrValidationFailed, report.CriticalChecks, report.WarningChecks)
	}
	return nil
}

func runValidation(ctx context.Context, repo validationRepository, opts ValidationOptions) (ValidationReport, error) {
	if opts.MaxSamples <= 0 {
		opts.MaxSamples = 5
	}

	report := ValidationReport{
		ExpectedSchemaVersion: fmt.Sprintf("%d.%d", model.SchemaMajor, model.SchemaMinor),
		Strict:                opts.Strict,
	}

	versionCheck, version, err := checkValidationSchemaVersion(repo)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = version
	report.Checks = append(report.Checks, versionCheck)

	for _, table := range []*model.TableMeta{
		model.TableKlineDaily,
		model.TableBasicDaily,
		model.TableAdjustFactor,
	} {
		check, err := checkDuplicateKeys(ctx, repo, table, opts.MaxSamples)
		if err != nil {
			return report, err
		}
		report.Checks = append(report.Checks, check)
	}

	precloseCheck, err := checkInvalidNumeric(
		ctx, repo, "basic_preclose_positive_finite", model.TableBasicDaily.TableName, "preclose", opts.MaxSamples,
	)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, precloseCheck)

	factorCheck, err := checkInvalidNumeric(
		ctx, repo, "factor_hfq_positive_finite", model.TableAdjustFactor.TableName, "hfq_factor", opts.MaxSamples,
	)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, factorCheck)

	missingFactorCheck, err := checkMissingFactors(ctx, repo, opts.MaxSamples)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, missingFactorCheck)

	latestCheck, err := checkLatestDateConsistency(ctx, repo, opts.MaxSamples)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, latestCheck)

	ohlcChecks, err := checkOHLC(ctx, repo, opts.MaxSamples)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, ohlcChecks...)

	cbondCheck, err := checkCBondPriceScale(ctx, repo, opts.MaxSamples)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, cbondCheck)

	presenceChecks, err := checkDataPresence(ctx, repo)
	if err != nil {
		return report, err
	}
	report.Checks = append(report.Checks, presenceChecks...)

	finalizeValidationReport(&report)
	return report, nil
}

func checkValidationSchemaVersion(repo validationRepository) (ValidationCheck, string, error) {
	check := ValidationCheck{Check: "schema_version"}
	actual, err := repo.ReadSchemaVersion()
	if err != nil {
		check.Severity = SeverityCritical
		check.Message = fmt.Sprintf("read schema version failed: %v", err)
		return check, actual, nil
	}
	if actual == "" {
		check.Severity = SeverityCritical
		check.Message = "schema version is missing"
		return check, actual, nil
	}

	dbMajor, err := strconv.Atoi(strings.SplitN(actual, ".", 2)[0])
	if err != nil {
		check.Severity = SeverityCritical
		check.Message = fmt.Sprintf("invalid schema version %q", actual)
		return check, actual, nil
	}
	if dbMajor != model.SchemaMajor {
		check.Severity = SeverityCritical
		check.Message = fmt.Sprintf("schema major %d, expected %d", dbMajor, model.SchemaMajor)
		return check, actual, nil
	}

	check.Severity = SeverityOK
	return check, actual, nil
}

func checkDuplicateKeys(
	ctx context.Context,
	repo validationRepository,
	table *model.TableMeta,
	maxSamples int,
) (ValidationCheck, error) {
	name := "duplicate_keys:" + table.TableName
	query := fmt.Sprintf(`
		/* check:duplicate_keys */
		SELECT symbol, MIN(date) AS date, COUNT(*) AS row_count, 0 AS value, '' AS detail
		FROM %s
		GROUP BY symbol, date
		HAVING COUNT(*) > 1
		ORDER BY symbol, date
	`, table.TableName)

	var rows []validationRow
	if err := repo.QueryRows(ctx, query, &rows); err != nil {
		return queryFailure(ctx, name, err), ctx.Err()
	}
	check := ValidationCheck{Check: name, Severity: SeverityOK}
	symbols := make(map[string]struct{})
	for _, row := range rows {
		check.Count += row.RowCount
		addSymbol(symbols, row.Symbol)
		appendValidationSample(&check, maxSamples, ValidationSample{
			Symbol: row.Symbol,
			Date:   formatValidationDate(row.Date),
			Details: map[string]string{
				"duplicate_rows": strconv.FormatInt(row.RowCount, 10),
			},
		})
	}
	if len(rows) > 0 {
		check.Severity = SeverityCritical
	}
	check.SymbolCount = int64(len(symbols))
	return check, nil
}

func checkInvalidNumeric(
	ctx context.Context,
	repo validationRepository,
	name string,
	table string,
	column string,
	maxSamples int,
) (ValidationCheck, error) {
	query := fmt.Sprintf(`
		/* check:%s */
		SELECT symbol, MIN(date) AS date, COUNT(*) AS row_count, MIN(%s) AS value, '' AS detail
		FROM %s
		WHERE %s <= 0 OR NOT isfinite(%s)
		GROUP BY symbol
		ORDER BY symbol
	`, name, column, table, column, column)

	var rows []validationRow
	if err := repo.QueryRows(ctx, query, &rows); err != nil {
		return queryFailure(ctx, name, err), ctx.Err()
	}
	check := ValidationCheck{Check: name, Severity: SeverityOK}
	symbols := make(map[string]struct{})
	for _, row := range rows {
		check.Count += row.RowCount
		addSymbol(symbols, row.Symbol)
		appendValidationSample(&check, maxSamples, ValidationSample{
			Symbol: row.Symbol,
			Date:   formatValidationDate(row.Date),
			Details: map[string]string{
				"invalid_rows": strconv.FormatInt(row.RowCount, 10),
				"min_value":    strconv.FormatFloat(row.Value, 'g', -1, 64),
			},
		})
	}
	if len(rows) > 0 {
		check.Severity = SeverityCritical
	}
	check.SymbolCount = int64(len(symbols))
	return check, nil
}

func checkMissingFactors(
	ctx context.Context,
	repo validationRepository,
	maxSamples int,
) (ValidationCheck, error) {
	name := "basic_missing_factor"
	query := fmt.Sprintf(`
		/* check:basic_missing_factor */
		SELECT b.symbol, MIN(b.date) AS date, COUNT(*) AS row_count, 0 AS value, '' AS detail
		FROM %s b
		LEFT JOIN %s f ON b.symbol = f.symbol AND b.date = f.date
		WHERE f.symbol IS NULL
		GROUP BY b.symbol
		ORDER BY b.symbol
	`, model.TableBasicDaily.TableName, model.TableAdjustFactor.TableName)

	var rows []validationRow
	if err := repo.QueryRows(ctx, query, &rows); err != nil {
		return queryFailure(ctx, name, err), ctx.Err()
	}
	check := ValidationCheck{Check: name, Severity: SeverityOK}
	symbols := make(map[string]struct{})
	for _, row := range rows {
		check.Count += row.RowCount
		addSymbol(symbols, row.Symbol)
		appendValidationSample(&check, maxSamples, ValidationSample{
			Symbol: row.Symbol,
			Date:   formatValidationDate(row.Date),
			Details: map[string]string{
				"missing_factor_rows": strconv.FormatInt(row.RowCount, 10),
			},
		})
	}
	if len(rows) > 0 {
		check.Severity = SeverityCritical
	}
	check.SymbolCount = int64(len(symbols))
	return check, nil
}

func checkLatestDateConsistency(
	ctx context.Context,
	repo validationRepository,
	maxSamples int,
) (ValidationCheck, error) {
	name := "latest_date_consistency:stock_etf_bstock"
	check := ValidationCheck{Check: name, Severity: SeverityOK}

	symbols, err := repo.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
	if err != nil {
		return queryFailure(ctx, name, err), nil
	}
	basicDates, err := repo.GetLatestDates(model.TableBasicDaily.TableName, "date")
	if err != nil {
		return queryFailure(ctx, name, err), nil
	}
	factorDates, err := repo.GetLatestDates(model.TableAdjustFactor.TableName, "date")
	if err != nil {
		return queryFailure(ctx, name, err), nil
	}

	affected := make(map[string]struct{})
	for _, symbol := range symbols {
		basicDate, hasBasic := basicDates[symbol]
		factorDate, hasFactor := factorDates[symbol]
		basicText := formatOptionalDate(basicDate, hasBasic)
		factorText := formatOptionalDate(factorDate, hasFactor)
		if hasBasic && hasFactor && basicText == factorText {
			continue
		}
		check.Count++
		addSymbol(affected, symbol)
		appendValidationSample(&check, maxSamples, ValidationSample{
			Symbol: symbol,
			Date:   basicText,
			Details: map[string]string{
				"basic_latest":  basicText,
				"factor_latest": factorText,
			},
		})
	}
	if check.Count > 0 {
		check.Severity = SeverityCritical
	}
	check.SymbolCount = int64(len(affected))
	return check, nil
}

func checkOHLC(ctx context.Context, repo validationRepository, maxSamples int) ([]ValidationCheck, error) {
	latestDate, err := repo.GetLatestDate(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest kline date: %w", err)
	}
	latest := ""
	if !latestDate.IsZero() {
		latest = latestDate.Format("2006-01-02")
	}

	query := fmt.Sprintf(`
		/* check:ohlc_anomalies */
		SELECT
			k.symbol,
			k.date,
			k.open,
			k.high,
			k.low,
			k.close,
			COALESCE(c.class, '%s') AS class,
			CASE WHEN NOT isfinite(k.open) OR NOT isfinite(k.high) OR NOT isfinite(k.low) OR NOT isfinite(k.close)
				OR k.open <= 0 OR k.high <= 0 OR k.low <= 0 OR k.close <= 0
				THEN 1 ELSE 0 END AS nonpositive,
			CASE WHEN k.high < k.low THEN 1 ELSE 0 END AS high_lt_low,
			CASE WHEN k.high < GREATEST(k.open, k.close) THEN 1 ELSE 0 END AS high_lt_max,
			CASE WHEN k.low > LEAST(k.open, k.close) + GREATEST(1e-9, ABS(LEAST(k.open, k.close)) * 1e-9)
				THEN 1 ELSE 0 END AS low_gt_min
		FROM %s k
		LEFT JOIN %s c ON k.symbol = c.symbol
		WHERE NOT isfinite(k.open) OR NOT isfinite(k.high) OR NOT isfinite(k.low) OR NOT isfinite(k.close)
			OR k.open <= 0 OR k.high <= 0 OR k.low <= 0 OR k.close <= 0
			OR k.high < k.low
			OR k.high < GREATEST(k.open, k.close)
			OR k.low > LEAST(k.open, k.close) + GREATEST(1e-9, ABS(LEAST(k.open, k.close)) * 1e-9)
		ORDER BY k.date, k.symbol
	`, model.ClassUnknown, model.TableKlineDaily.TableName, model.TableSymbolClass.TableName)

	var rows []ohlcValidationRow
	if err := repo.QueryRows(ctx, query, &rows); err != nil {
		return nil, err
	}

	checks := []ValidationCheck{
		{Check: "ohlc_nonpositive", Severity: SeverityOK},
		{Check: "ohlc_high_lt_low", Severity: SeverityOK},
		{Check: "ohlc_high_lt_open_close", Severity: SeverityOK},
		{Check: "ohlc_low_gt_open_close", Severity: SeverityOK},
	}
	symbols := make([]map[string]struct{}, len(checks))
	for i := range symbols {
		symbols[i] = make(map[string]struct{})
	}

	for _, row := range rows {
		severity := ohlcSeverity(row, latest)
		details := map[string]string{
			"class": row.Class,
			"open":  strconv.FormatFloat(row.Open, 'g', -1, 64),
			"high":  strconv.FormatFloat(row.High, 'g', -1, 64),
			"low":   strconv.FormatFloat(row.Low, 'g', -1, 64),
			"close": strconv.FormatFloat(row.Close, 'g', -1, 64),
		}
		flags := []int{row.NonPositive, row.HighLTLow, row.HighLTMax, row.LowGTMin}
		for i, flag := range flags {
			if flag == 0 {
				continue
			}
			checks[i].Count++
			addSymbol(symbols[i], row.Symbol)
			raiseSeverity(&checks[i].Severity, severity)
			appendValidationSample(&checks[i], maxSamples, ValidationSample{
				Symbol:  row.Symbol,
				Date:    row.Date.Format("2006-01-02"),
				Details: details,
			})
		}
	}
	for i := range checks {
		checks[i].SymbolCount = int64(len(symbols[i]))
	}
	return checks, nil
}

func checkCBondPriceScale(
	ctx context.Context,
	repo validationRepository,
	maxSamples int,
) (ValidationCheck, error) {
	name := "cbond_price_scale_sample"
	query := fmt.Sprintf(`
		/* check:cbond_price_scale_sample */
		WITH latest_dates AS (
			SELECT symbol, MAX(date) AS latest_date
			FROM %s
			GROUP BY symbol
		)
		SELECT k.symbol, k.date, 1 AS row_count, k.close AS value, '' AS detail
		FROM %s k
		INNER JOIN latest_dates l ON k.symbol = l.symbol AND k.date = l.latest_date
		INNER JOIN %s c ON k.symbol = c.symbol
		WHERE c.class = '%s'
			AND (
				k.close > 5000
				OR (k.symbol = 'sh113001' AND k.close > 1000)
			)
		ORDER BY k.symbol
	`,
		model.TableKlineDaily.TableName,
		model.TableKlineDaily.TableName,
		model.TableSymbolClass.TableName,
		model.ClassCBond,
	)

	var rows []validationRow
	if err := repo.QueryRows(ctx, query, &rows); err != nil {
		return queryFailure(ctx, name, err), ctx.Err()
	}
	check := ValidationCheck{Check: name, Severity: SeverityOK}
	symbols := make(map[string]struct{})
	for _, row := range rows {
		check.Count += row.RowCount
		addSymbol(symbols, row.Symbol)
		appendValidationSample(&check, maxSamples, ValidationSample{
			Symbol: row.Symbol,
			Date:   formatValidationDate(row.Date),
			Details: map[string]string{
				"close": strconv.FormatFloat(row.Value, 'g', -1, 64),
			},
		})
	}
	if len(rows) > 0 {
		check.Severity = SeverityCritical
	}
	check.SymbolCount = int64(len(symbols))
	return check, nil
}

func checkDataPresence(ctx context.Context, repo validationRepository) ([]ValidationCheck, error) {
	tables := []struct {
		name     string
		table    string
		severity ValidationSeverity
	}{
		{"data_presence:raw_symbol_class", model.TableSymbolClass.TableName, SeverityCritical},
		{"data_presence:raw_symbol_name", model.TableSymbolName.TableName, SeverityWarning},
		{"data_presence:raw_tdx_blocks_info", model.TableBlockInfo.TableName, SeverityWarning},
		{"data_presence:raw_tdx_blocks_member", model.TableBlockMember.TableName, SeverityWarning},
	}

	checks := make([]ValidationCheck, 0, len(tables))
	for _, item := range tables {
		query := fmt.Sprintf(`
			/* check:data_presence */
			SELECT COUNT(*) AS row_count FROM %s
		`, item.table)
		var rows []validationRow
		if err := repo.QueryRows(ctx, query, &rows); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			checks = append(checks, queryFailure(ctx, item.name, err))
			continue
		}

		check := ValidationCheck{Check: item.name, Severity: SeverityOK}
		if len(rows) == 0 || rows[0].RowCount == 0 {
			check.Severity = item.severity
			check.Message = "table has no rows"
		} else {
			check.Count = rows[0].RowCount
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func ohlcSeverity(row ohlcValidationRow, latestDate string) ValidationSeverity {
	if row.Class == model.ClassStock || row.Class == model.ClassETF {
		return SeverityCritical
	}
	if latestDate != "" && row.Date.Format("2006-01-02") == latestDate {
		return SeverityCritical
	}
	return SeverityWarning
}

func raiseSeverity(current *ValidationSeverity, next ValidationSeverity) {
	if next == SeverityCritical {
		*current = SeverityCritical
		return
	}
	if *current == "" || *current == SeverityOK {
		*current = SeverityWarning
	}
}

func appendValidationSample(check *ValidationCheck, maxSamples int, sample ValidationSample) {
	if len(check.Samples) >= maxSamples {
		return
	}
	check.Samples = append(check.Samples, sample)
}

func addSymbol(symbols map[string]struct{}, symbol string) {
	if symbol != "" {
		symbols[symbol] = struct{}{}
	}
}

func queryFailure(ctx context.Context, name string, err error) ValidationCheck {
	check := ValidationCheck{
		Check:    name,
		Severity: SeverityCritical,
		Message:  err.Error(),
	}
	if ctx.Err() != nil {
		check.Message = ctx.Err().Error()
	}
	return check
}

func formatValidationDate(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.Format("2006-01-02")
}

func formatOptionalDate(value time.Time, ok bool) string {
	if !ok || value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02")
}

func finalizeValidationReport(report *ValidationReport) {
	report.CriticalChecks = 0
	report.WarningChecks = 0
	for i := range report.Checks {
		check := &report.Checks[i]
		if check.Severity == "" {
			check.Severity = SeverityOK
		}
		check.Passed = check.Severity == SeverityOK ||
			(check.Severity == SeverityWarning && !report.Strict)
		switch check.Severity {
		case SeverityCritical:
			report.CriticalChecks++
		case SeverityWarning:
			report.WarningChecks++
		}
	}
	report.Passed = report.CriticalChecks == 0 &&
		(!report.Strict || report.WarningChecks == 0)
}

func writeValidationJSON(w io.Writer, report ValidationReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("failed to write validation json: %w", err)
	}
	return nil
}

func writeValidationTable(w io.Writer, report ValidationReport) error {
	fmt.Fprintf(w, "schema: %s (expected %s)\n", report.SchemaVersion, report.ExpectedSchemaVersion)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSEVERITY\tCOUNT\tSYMBOLS\tPASSED\tSAMPLES")
	for _, check := range report.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%t\t%s\n",
			check.Check,
			check.Severity,
			check.Count,
			check.SymbolCount,
			check.Passed,
			formatValidationSamples(check.Samples),
		)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to write validation table: %w", err)
	}
	fmt.Fprintf(w, "passed: %t (critical=%d, warning=%d)\n",
		report.Passed, report.CriticalChecks, report.WarningChecks)
	return nil
}

func formatValidationSamples(samples []ValidationSample) string {
	if len(samples) == 0 {
		return "-"
	}
	formatted := make([]string, 0, len(samples))
	for _, sample := range samples {
		parts := []string{}
		if sample.Symbol != "" {
			parts = append(parts, sample.Symbol)
		}
		if sample.Date != "" {
			parts = append(parts, sample.Date)
		}
		keys := make([]string, 0, len(sample.Details))
		for key := range sample.Details {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, key+"="+sample.Details[key])
		}
		formatted = append(formatted, strings.Join(parts, ":"))
	}
	return strings.Join(formatted, "; ")
}
