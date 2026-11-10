package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func (d *ClickHouseDriver) Query(table string, conditions map[string]interface{}, dest interface{}) error {
	query := fmt.Sprintf("SELECT * FROM %s", table)
	args := []interface{}{}
	if len(conditions) > 0 {
		whereParts := []string{}
		for k, v := range conditions {
			whereParts = append(whereParts, fmt.Sprintf("%s = ?", k))
			args = append(args, v)
		}
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}

	return d.db.Select(dest, query, args...)
}

func (d *ClickHouseDriver) QueryRows(ctx context.Context, query string, dest interface{}) error {
	return d.db.SelectContext(ctx, dest, query)
}

func (d *ClickHouseDriver) GetLatestDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT toDate(maxOrNull(%s)) AS latest FROM %s", dateCol, tableName)
	var latest sql.NullTime
	err := d.db.Get(&latest, query)
	if err != nil {
		return time.Time{}, err
	}
	if !latest.Valid {
		return time.Time{}, nil
	}
	return latest.Time, nil
}

func (d *ClickHouseDriver) GetLatestDates(tableName string, dateCol string) (map[string]time.Time, error) {
	query := fmt.Sprintf("SELECT symbol, max(%s) AS latest FROM %s GROUP BY symbol", dateCol, tableName)

	var rows []struct {
		Symbol string       `db:"symbol"`
		Latest sql.NullTime `db:"latest"`
	}
	if err := d.db.Select(&rows, query); err != nil {
		return nil, fmt.Errorf("failed to query latest dates: %w", err)
	}

	result := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if row.Latest.Valid {
			result[row.Symbol] = row.Latest.Time
		}
	}
	return result, nil
}

func (d *ClickHouseDriver) GetMinDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT toDate(minOrNull(%s)) AS earliest FROM %s", dateCol, tableName)
	var earliest sql.NullTime
	err := d.db.Get(&earliest, query)
	if err != nil {
		return time.Time{}, err
	}
	if !earliest.Valid {
		return time.Time{}, nil
	}
	return earliest.Time, nil
}

func (d *ClickHouseDriver) GetSymbolsByClass(classes ...string) ([]string, error) {
	if len(classes) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(classes))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(
		"SELECT symbol FROM %s WHERE class IN (%s) ORDER BY symbol",
		model.TableSymbolClass.TableName, placeholders,
	)
	args := make([]any, len(classes))
	for i, c := range classes {
		args[i] = c
	}

	var symbols []string
	if err := d.db.Select(&symbols, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query symbols by class: %w", err)
	}
	return symbols, nil
}

func (d *ClickHouseDriver) CountKlineDaily() (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", model.TableKlineDaily.TableName)

	var count int64
	err := d.db.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("failed to count kline daily: %w", err)
	}

	return count, nil
}

func (d *ClickHouseDriver) QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error) {
	conditions := []string{"symbol = ?"}
	args := []interface{}{symbol}

	if startDate != nil {
		conditions = append(conditions, "date >= ?")
		args = append(args, *startDate)
	}
	if endDate != nil {
		conditions = append(conditions, "date <= ?")
		args = append(args, *endDate)
	}

	query := fmt.Sprintf(
		`SELECT * FROM %s WHERE %s ORDER BY date ASC`,
		model.TableKlineDaily.TableName,
		strings.Join(conditions, " AND "),
	)

	var results []model.KlineDay
	if err := d.db.Select(&results, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query kline daily: %w", err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error) {
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE symbol = ? ORDER BY date",
		model.TableBasicDaily.TableName,
	)

	var results []model.BasicDaily
	if err := d.db.Select(&results, query, symbol); err != nil {
		return nil, fmt.Errorf("failed to query daily basics by symbol %s: %w", symbol, err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetLatestBasics(classes ...string) (map[string]model.BasicDaily, error) {
	placeholders, args := classQueryArgs(classes)
	query := fmt.Sprintf(`
		SELECT b.*
		FROM %s b
		INNER JOIN (
			SELECT symbol, MAX(date) AS latest
			FROM %s GROUP BY symbol
		) latest ON b.symbol = latest.symbol AND b.date = latest.latest
		INNER JOIN %s c ON b.symbol = c.symbol
		WHERE c.class IN (%s)
	`, model.TableBasicDaily.TableName, model.TableBasicDaily.TableName,
		model.TableSymbolClass.TableName, placeholders)

	var rows []model.BasicDaily
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query latest basics: %w", err)
	}
	result := make(map[string]model.BasicDaily, len(rows))
	for _, row := range rows {
		result[row.Symbol] = row
	}
	return result, nil
}

func (d *ClickHouseDriver) GetKlineDailyAfterLatestBasic(classes ...string) (map[string][]model.KlineDay, error) {
	placeholders, args := classQueryArgs(classes)
	query := fmt.Sprintf(`
		SELECT k.*
		FROM %s k
		INNER JOIN (
			SELECT symbol, MAX(date) AS latest
			FROM %s GROUP BY symbol
		) b ON k.symbol = b.symbol
		INNER JOIN %s c ON k.symbol = c.symbol
		WHERE k.date > b.latest AND c.class IN (%s)
		ORDER BY k.symbol, k.date
	`, model.TableKlineDaily.TableName, model.TableBasicDaily.TableName,
		model.TableSymbolClass.TableName, placeholders)

	var rows []model.KlineDay
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query kline after latest basic: %w", err)
	}
	result := make(map[string][]model.KlineDay)
	for _, row := range rows {
		result[row.Symbol] = append(result[row.Symbol], row)
	}
	return result, nil
}

func (d *ClickHouseDriver) GetLatestFactor(symbol string) (*model.Factor, error) {
	query := fmt.Sprintf(
		`SELECT symbol, date, hfq_factor FROM %s
		 WHERE symbol = ? ORDER BY date DESC LIMIT 1`,
		model.TableAdjustFactor.TableName,
	)

	var factor model.Factor
	if err := d.db.Get(&factor, query, symbol); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query latest factor for %s: %w", symbol, err)
	}
	return &factor, nil
}

func (d *ClickHouseDriver) GetLatestFactors(classes ...string) (map[string]model.Factor, error) {
	placeholders, args := classQueryArgs(classes)
	query := fmt.Sprintf(`
		SELECT f.*
		FROM %s f
		INNER JOIN (
			SELECT symbol, MAX(date) AS latest
			FROM %s GROUP BY symbol
		) latest ON f.symbol = latest.symbol AND f.date = latest.latest
		INNER JOIN %s c ON f.symbol = c.symbol
		WHERE c.class IN (%s)
	`, model.TableAdjustFactor.TableName, model.TableAdjustFactor.TableName,
		model.TableSymbolClass.TableName, placeholders)

	var rows []model.Factor
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query latest factors: %w", err)
	}
	result := make(map[string]model.Factor, len(rows))
	for _, row := range rows {
		result[row.Symbol] = row
	}
	return result, nil
}

func (d *ClickHouseDriver) GetBasicsAfterLatestFactor(classes ...string) (map[string][]model.BasicDaily, error) {
	placeholders, args := classQueryArgs(classes)
	query := fmt.Sprintf(`
		SELECT b.*
		FROM %s b
		INNER JOIN (
			SELECT symbol, MAX(date) AS latest
			FROM %s GROUP BY symbol
		) f ON b.symbol = f.symbol
		INNER JOIN %s c ON b.symbol = c.symbol
		WHERE b.date >= f.latest AND c.class IN (%s)
		ORDER BY b.symbol, b.date
	`, model.TableBasicDaily.TableName, model.TableAdjustFactor.TableName,
		model.TableSymbolClass.TableName, placeholders)

	var rows []model.BasicDaily
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query basics after latest factor: %w", err)
	}
	result := make(map[string][]model.BasicDaily)
	for _, row := range rows {
		result[row.Symbol] = append(result[row.Symbol], row)
	}
	return result, nil
}

func classQueryArgs(classes []string) (string, []any) {
	if len(classes) == 0 {
		return "SELECT NULL WHERE FALSE", nil
	}
	placeholders := strings.Repeat("?,", len(classes))
	args := make([]any, len(classes))
	for i, class := range classes {
		args[i] = class
	}
	return placeholders[:len(placeholders)-1], args
}

func (d *ClickHouseDriver) HasInvalidFactors() (bool, error) {
	query := fmt.Sprintf(
		"SELECT count() FROM %s WHERE hfq_factor <= 0 OR NOT isFinite(hfq_factor)",
		model.TableAdjustFactor.TableName,
	)

	var count int64
	if err := d.db.Get(&count, query); err != nil {
		return false, fmt.Errorf("failed to validate factors: %w", err)
	}
	return count > 0, nil
}

func (d *ClickHouseDriver) GetInvalidFactorSymbols() ([]string, error) {
	query := fmt.Sprintf(
		`SELECT DISTINCT symbol FROM %s
		 WHERE hfq_factor <= 0 OR NOT isFinite(hfq_factor)
		 ORDER BY symbol`,
		model.TableAdjustFactor.TableName,
	)

	var symbols []string
	if err := d.db.Select(&symbols, query); err != nil {
		return nil, fmt.Errorf("failed to query invalid factor symbols: %w", err)
	}
	return symbols, nil
}

func (d *ClickHouseDriver) GetGbbq() ([]model.GbbqData, error) {
	table := model.TableGbbq.TableName

	query := fmt.Sprintf(`SELECT * FROM %s ORDER BY symbol, date`, table)

	var results []model.GbbqData
	if err := d.db.Select(&results, query); err != nil {
		return nil, fmt.Errorf("failed to query gbbq: %w", err)
	}

	return results, nil
}

func (d *ClickHouseDriver) GetHolidays() ([]time.Time, error) {
	query := fmt.Sprintf("SELECT date FROM %s ORDER BY date", model.TableHoliday.TableName)
	var dates []time.Time
	if err := d.db.Select(&dates, query); err != nil {
		return nil, fmt.Errorf("failed to query holidays: %w", err)
	}
	return dates, nil
}
