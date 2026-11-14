package duckdb

import (
	"fmt"
	"strings"

	"github.com/jing2uo/tdx2db/model"
)

func (d *DuckDBDriver) ImportCSV(meta *model.TableMeta, csvPath string) error {
	var colMaps []string
	for _, col := range meta.Columns {
		duckType := d.mapType(col.Type)
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, duckType))
	}

	columnsStr := strings.Join(colMaps, ", ")

	query := fmt.Sprintf(`
		INSERT INTO %s
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d',
			timestampformat='%%Y-%%m-%%d %%H:%%M'
		)
	`, meta.TableName, csvPath, columnsStr)

	_, err := d.db.Exec(query)
	return err
}

func (d *DuckDBDriver) TruncateTable(meta *model.TableMeta) error {
	query := fmt.Sprintf("DELETE FROM %s", meta.TableName)
	if _, err := d.db.Exec(query); err != nil {
		return fmt.Errorf("duckdb truncate failed: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportKlineDaily(path string) error {
	var colMaps []string
	for _, col := range model.TableKlineDaily.Columns {
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, d.mapType(col.Type)))
	}

	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin kline import: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_kline_daily"); err != nil {
		return fmt.Errorf("failed to clear kline staging table: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_kline_daily AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d'
		)
	`, path, strings.Join(colMaps, ", "))); err != nil {
		return fmt.Errorf("failed to stage kline csv: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM %s target
		USING incoming_kline_daily incoming
		WHERE target.symbol = incoming.symbol AND target.date = incoming.date
	`, model.TableKlineDaily.TableName)); err != nil {
		return fmt.Errorf("failed to replace existing kline rows: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO %s
		SELECT DISTINCT * FROM incoming_kline_daily
	`, model.TableKlineDaily.TableName)); err != nil {
		return fmt.Errorf("failed to insert kline rows: %w", err)
	}

	if _, err := tx.Exec("DROP TABLE incoming_kline_daily"); err != nil {
		return fmt.Errorf("failed to drop kline staging table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit kline import: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportKline1Min(path string) error {
	return d.ImportCSV(model.TableKline1Min, path)
}

func (d *DuckDBDriver) ImportGBBQ(path string) error {
	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin gbbq import: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_gbbq"); err != nil {
		return fmt.Errorf("failed to clear gbbq staging table: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_gbbq AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d'
		)
	`, path, duckDBCSVColumns(d, model.TableGbbq))); err != nil {
		return fmt.Errorf("failed to stage gbbq csv: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM " + model.TableGbbq.TableName); err != nil {
		return fmt.Errorf("failed to clear gbbq table: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO %s
		SELECT DISTINCT * FROM incoming_gbbq
	`, model.TableGbbq.TableName)); err != nil {
		return fmt.Errorf("failed to insert gbbq rows: %w", err)
	}
	if _, err := tx.Exec("DROP TABLE incoming_gbbq"); err != nil {
		return fmt.Errorf("failed to drop gbbq staging table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit gbbq import: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportBasic(path string, rebuildSymbols []string) error {
	var colMaps []string
	for _, col := range model.TableBasicDaily.Columns {
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, d.mapType(col.Type)))
	}

	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin basic import: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_basic_daily"); err != nil {
		return fmt.Errorf("failed to clear basic staging table: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_basic_daily AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d'
		)
	`, path, strings.Join(colMaps, ", "))); err != nil {
		return fmt.Errorf("failed to stage basic csv: %w", err)
	}

	if len(rebuildSymbols) > 0 {
		if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_basic_rebuild_symbols"); err != nil {
			return fmt.Errorf("failed to clear basic rebuild symbol staging table: %w", err)
		}
		if _, err := tx.Exec("CREATE TEMP TABLE incoming_basic_rebuild_symbols (symbol VARCHAR)"); err != nil {
			return fmt.Errorf("failed to create basic rebuild symbol staging table: %w", err)
		}
		stmt, err := tx.Preparex("INSERT INTO incoming_basic_rebuild_symbols (symbol) VALUES (?)")
		if err != nil {
			return fmt.Errorf("failed to prepare basic rebuild symbols: %w", err)
		}
		for _, symbol := range rebuildSymbols {
			if _, err := stmt.Exec(symbol); err != nil {
				stmt.Close()
				return fmt.Errorf("failed to stage basic rebuild symbol %s: %w", symbol, err)
			}
		}
		if err := stmt.Close(); err != nil {
			return fmt.Errorf("failed to close basic rebuild symbol statement: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`
			DELETE FROM %s target
			WHERE target.symbol IN (
				SELECT symbol FROM incoming_basic_rebuild_symbols
			)
		`, model.TableBasicDaily.TableName)); err != nil {
			return fmt.Errorf("failed to replace rebuilt basic symbols: %w", err)
		}
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM %s target
		USING incoming_basic_daily incoming
		WHERE target.symbol = incoming.symbol AND target.date = incoming.date
	`, model.TableBasicDaily.TableName)); err != nil {
		return fmt.Errorf("failed to replace existing basic rows: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO %s
		SELECT DISTINCT * FROM incoming_basic_daily
	`, model.TableBasicDaily.TableName)); err != nil {
		return fmt.Errorf("failed to insert basic rows: %w", err)
	}

	if _, err := tx.Exec("DROP TABLE incoming_basic_daily"); err != nil {
		return fmt.Errorf("failed to drop basic staging table: %w", err)
	}
	if len(rebuildSymbols) > 0 {
		if _, err := tx.Exec("DROP TABLE incoming_basic_rebuild_symbols"); err != nil {
			return fmt.Errorf("failed to drop basic rebuild symbol staging table: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit basic import: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportAdjustFactors(path string, rebuildSymbols []string) error {
	var colMaps []string
	for _, col := range model.TableAdjustFactor.Columns {
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, d.mapType(col.Type)))
	}

	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin factor import: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_adjust_factor"); err != nil {
		return fmt.Errorf("failed to clear factor staging table: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_adjust_factor AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s},
			dateformat='%%Y-%%m-%%d'
		)
	`, path, strings.Join(colMaps, ", "))); err != nil {
		return fmt.Errorf("failed to stage factor csv: %w", err)
	}

	if len(rebuildSymbols) > 0 {
		if _, err := tx.Exec("DROP TABLE IF EXISTS incoming_factor_rebuild_symbols"); err != nil {
			return fmt.Errorf("failed to clear factor rebuild symbol staging table: %w", err)
		}
		if _, err := tx.Exec("CREATE TEMP TABLE incoming_factor_rebuild_symbols (symbol VARCHAR)"); err != nil {
			return fmt.Errorf("failed to create factor rebuild symbol staging table: %w", err)
		}
		stmt, err := tx.Preparex("INSERT INTO incoming_factor_rebuild_symbols (symbol) VALUES (?)")
		if err != nil {
			return fmt.Errorf("failed to prepare factor rebuild symbols: %w", err)
		}
		for _, symbol := range rebuildSymbols {
			if _, err := stmt.Exec(symbol); err != nil {
				stmt.Close()
				return fmt.Errorf("failed to stage factor rebuild symbol %s: %w", symbol, err)
			}
		}
		if err := stmt.Close(); err != nil {
			return fmt.Errorf("failed to close factor rebuild symbol statement: %w", err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`
			DELETE FROM %s target
			WHERE target.symbol IN (
				SELECT symbol FROM incoming_factor_rebuild_symbols
			)
		`, model.TableAdjustFactor.TableName)); err != nil {
			return fmt.Errorf("failed to replace rebuilt factor symbols: %w", err)
		}
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM %s target
		USING incoming_adjust_factor incoming
		WHERE target.symbol = incoming.symbol AND target.date = incoming.date
	`, model.TableAdjustFactor.TableName)); err != nil {
		return fmt.Errorf("failed to replace existing factor rows: %w", err)
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO %s
		SELECT DISTINCT * FROM incoming_adjust_factor
	`, model.TableAdjustFactor.TableName)); err != nil {
		return fmt.Errorf("failed to insert factor rows: %w", err)
	}

	if _, err := tx.Exec("DROP TABLE incoming_adjust_factor"); err != nil {
		return fmt.Errorf("failed to drop factor staging table: %w", err)
	}
	if len(rebuildSymbols) > 0 {
		if _, err := tx.Exec("DROP TABLE incoming_factor_rebuild_symbols"); err != nil {
			return fmt.Errorf("failed to drop factor rebuild symbol staging table: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit factor import: %w", err)
	}
	return nil
}

func (d *DuckDBDriver) ImportHolidays(path string) error {
	d.TruncateTable(model.TableHoliday)
	return d.ImportCSV(model.TableHoliday, path)
}

func (d *DuckDBDriver) ImportBlockSnapshot(infoPath, memberPath string) error {
	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin block import: %w", err)
	}
	defer tx.Rollback()

	for _, staging := range []string{"incoming_block_info", "incoming_block_member"} {
		if _, err := tx.Exec("DROP TABLE IF EXISTS " + staging); err != nil {
			return fmt.Errorf("failed to clear block staging table %s: %w", staging, err)
		}
	}

	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_block_info AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s}
		)
	`, infoPath, duckDBCSVColumns(d, model.TableBlockInfo))); err != nil {
		return fmt.Errorf("failed to stage block info csv: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE incoming_block_member AS
		SELECT * FROM read_csv('%s',
			header=true,
			columns={%s}
		)
	`, memberPath, duckDBCSVColumns(d, model.TableBlockMember))); err != nil {
		return fmt.Errorf("failed to stage block member csv: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM " + model.TableBlockInfo.TableName); err != nil {
		return fmt.Errorf("failed to clear block info: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM " + model.TableBlockMember.TableName); err != nil {
		return fmt.Errorf("failed to clear block members: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(
		`INSERT INTO %s
		 SELECT DISTINCT block_type, block_name, block_symbol, block_code,
			COALESCE(parent_code, ''), block_level
		 FROM incoming_block_info`,
		model.TableBlockInfo.TableName,
	)); err != nil {
		return fmt.Errorf("failed to insert block info: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(
		"INSERT INTO %s SELECT DISTINCT * FROM incoming_block_member",
		model.TableBlockMember.TableName,
	)); err != nil {
		return fmt.Errorf("failed to insert block members: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit block import: %w", err)
	}
	return nil
}

func duckDBCSVColumns(d *DuckDBDriver, meta *model.TableMeta) string {
	colMaps := make([]string, 0, len(meta.Columns))
	for _, col := range meta.Columns {
		colMaps = append(colMaps, fmt.Sprintf("'%s': '%s'", col.Name, d.mapType(col.Type)))
	}
	return strings.Join(colMaps, ", ")
}

func (d *DuckDBDriver) ImportSymbolNames(path string) error {
	d.TruncateTable(model.TableSymbolName)
	return d.ImportCSV(model.TableSymbolName, path)
}

// RebuildSymbolClass 全量重建 symbol_class 表（从 raw_kline_daily 取 distinct symbol 后归类）。
// 写入逻辑跟 Import 系列同性质，放这里。
func (d *DuckDBDriver) RebuildSymbolClass() error {
	kline := model.TableKlineDaily.TableName
	class := model.TableSymbolClass.TableName

	var codes []string
	if err := d.db.Select(&codes, fmt.Sprintf("SELECT DISTINCT symbol FROM %s", kline)); err != nil {
		return fmt.Errorf("failed to collect symbols: %w", err)
	}

	tx, err := d.db.Beginx()
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf("DELETE FROM %s", class)); err != nil {
		return fmt.Errorf("failed to clear symbol_class: %w", err)
	}

	stmt, err := tx.Preparex(fmt.Sprintf("INSERT INTO %s (symbol, class) VALUES (?, ?)", class))
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range codes {
		if _, err := stmt.Exec(c, model.ClassifyCode(c)); err != nil {
			return fmt.Errorf("failed to insert class for %s: %w", c, err)
		}
	}

	return tx.Commit()
}
