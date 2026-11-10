package model

import (
	"fmt"
	"strings"
	"sync"
)

// ViewDef 描述一个数据库视图：名字 + 每种方言的 SELECT 主体 SQL。
// 与 TableMeta 一样是纯数据、dialect 无关——具体取哪一栏、怎么拼 CREATE VIEW
// 由各 driver 自己决定（对应 driver 里的 mapType）。声明即自注册到 model registry。
type ViewDef struct {
	Name       string
	DuckDB     string
	ClickHouse string
}

var (
	viewRegistry   []ViewDef
	viewRegistryMu sync.Mutex

	deprecatedViewNames = []string{
		"v_stock_bfq",
		"v_etf_bfq",
		"v_bstock_bfq",
		"v_index_bfq",
	}
)

// DefineView 注册一个视图并原样返回，供 var 声明处持有。
func DefineView(view ViewDef) ViewDef {
	viewRegistryMu.Lock()
	defer viewRegistryMu.Unlock()

	viewRegistry = append(viewRegistry, view)
	return view
}

// AllViews 返回当前所有已注册的视图定义
func AllViews() []ViewDef {
	viewRegistryMu.Lock()
	defer viewRegistryMu.Unlock()

	result := make([]ViewDef, len(viewRegistry))
	copy(result, viewRegistry)
	return result
}

// DeprecatedViewNames 返回需要在 InitSchema 中清理的旧视图名。
func DeprecatedViewNames() []string {
	result := make([]string, len(deprecatedViewNames))
	copy(result, deprecatedViewNames)
	return result
}

// --- 定义视图 ---
//
// 命名约定：v_<class>_<fq>，class 放前面便于 tab-complete 按归属浏览
// （v_stock_<TAB> / v_etf_<TAB> / v_bstock_<TAB> 各列出 qfq/hfq，v_index 单独提供指数原始日线）。
// stock / etf / bstock 拆开维护：ETF/B 股价格 scale=1000、ROUND 精度 3 位，
// stock scale=100、ROUND 精度 2 位。ETF 视图不暴露 turnover/floatmv/totalmv。
var (
	ViewStockQFQ  = DefineView(adjustedQFQView("v_stock_qfq", ClassStock, 2, true))
	ViewStockHFQ  = DefineView(adjustedHFQView("v_stock_hfq", ClassStock, 2, true))
	ViewETFQFQ    = DefineView(adjustedQFQView("v_etf_qfq", ClassETF, 3, false))
	ViewETFHFQ    = DefineView(adjustedHFQView("v_etf_hfq", ClassETF, 3, false))
	ViewBStockQFQ = DefineView(adjustedQFQView("v_bstock_qfq", ClassBStock, 3, true))
	ViewBStockHFQ = DefineView(adjustedHFQView("v_bstock_hfq", ClassBStock, 3, true))
	ViewIndex     = DefineView(indexView("v_index"))
)

func indexView(name string) ViewDef {
	sql := fmt.Sprintf(`
		SELECT
			s.symbol     AS symbol,
			s.date       AS date,
			ROUND(s.open, 2) AS open,
			ROUND(s.high, 2) AS high,
			ROUND(s.low, 2) AS low,
			ROUND(s.close, 2) AS close,
			s.volume     AS volume,
			s.amount     AS amount,
			s.up_count   AS up_count,
			s.down_count AS down_count
		FROM %s s
		INNER JOIN %s sc ON s.symbol = sc.symbol AND sc.class = '%s'
	`,
		TableKlineDaily.TableName,
		TableSymbolClass.TableName,
		ClassIndex,
	)
	return ViewDef{Name: name, DuckDB: sql, ClickHouse: sql}
}

func adjustedHFQView(name, class string, precision int, includeMarketData bool) ViewDef {
	sql := fmt.Sprintf(`
		SELECT
			s.symbol AS symbol,
			s.date   AS date,
			ROUND(s.open  * COALESCE(f.hfq_factor, 1), %d) AS open,
			ROUND(s.high  * COALESCE(f.hfq_factor, 1), %d) AS high,
			ROUND(s.low   * COALESCE(f.hfq_factor, 1), %d) AS low,
			ROUND(s.close * COALESCE(f.hfq_factor, 1), %d) AS close,
			ROUND(b.preclose * COALESCE(f.hfq_factor, 1), %d) AS preclose,
			s.volume     AS volume,
			s.amount     AS amount,
			%s,
			COALESCE(f.hfq_factor, 1) AS hfq_factor
		FROM %s s
		INNER JOIN %s sc ON s.symbol = sc.symbol AND sc.class = '%s'
		LEFT JOIN %s f ON s.symbol = f.symbol AND s.date = f.date
		LEFT JOIN %s b ON s.symbol = b.symbol AND s.date = b.date
	`,
		precision,
		precision,
		precision,
		precision,
		precision,
		basicViewColumns(includeMarketData),
		TableKlineDaily.TableName,
		TableSymbolClass.TableName,
		class,
		TableAdjustFactor.TableName,
		TableBasicDaily.TableName,
	)
	return ViewDef{Name: name, DuckDB: sql, ClickHouse: sql}
}

func adjustedQFQView(name, class string, precision int, includeMarketData bool) ViewDef {
	return ViewDef{
		Name:       name,
		DuckDB:     adjustedQFQViewSQL(class, precision, true, includeMarketData),
		ClickHouse: adjustedQFQViewSQL(class, precision, false, includeMarketData),
	}
}

func adjustedQFQViewSQL(class string, precision int, scalarLatest bool, includeMarketData bool) string {
	cte := fmt.Sprintf(`
		WITH latest_factors AS (
			SELECT symbol, argMax(hfq_factor, date) AS latest_hfq
			FROM %s
			GROUP BY symbol
		)`, TableAdjustFactor.TableName)
	latest := "COALESCE(lf.latest_hfq, 1)"
	latestJoin := "LEFT JOIN latest_factors lf ON s.symbol = lf.symbol"
	if scalarLatest {
		cte = ""
		latest = fmt.Sprintf(
			"COALESCE((SELECT lf.hfq_factor FROM %s lf WHERE lf.symbol = s.symbol ORDER BY lf.date DESC LIMIT 1), 1)",
			TableAdjustFactor.TableName,
		)
		latestJoin = ""
	}
	factor := "COALESCE(f.hfq_factor, 1) / " + latest

	return fmt.Sprintf(`%s
		SELECT
			s.symbol AS symbol,
			s.date   AS date,
			ROUND(s.open  * %s, %d) AS open,
			ROUND(s.high  * %s, %d) AS high,
			ROUND(s.low   * %s, %d) AS low,
			ROUND(s.close * %s, %d) AS close,
			ROUND(b.preclose * %s, %d) AS preclose,
			s.volume     AS volume,
			s.amount     AS amount,
			%s,
			%s AS qfq_factor
		FROM %s s
		INNER JOIN %s sc ON s.symbol = sc.symbol AND sc.class = '%s'
		LEFT JOIN %s f ON s.symbol = f.symbol AND s.date = f.date
		%s
		LEFT JOIN %s b ON s.symbol = b.symbol AND s.date = b.date
	`,
		cte,
		factor,
		precision,
		factor,
		precision,
		factor,
		precision,
		factor,
		precision,
		factor,
		precision,
		basicViewColumns(includeMarketData),
		factor,
		TableKlineDaily.TableName,
		TableSymbolClass.TableName,
		class,
		TableAdjustFactor.TableName,
		latestJoin,
		TableBasicDaily.TableName,
	)
}

func basicViewColumns(includeMarketData bool) string {
	columns := make([]string, 0, 5)
	if includeMarketData {
		columns = append(columns,
			"b.turnover   AS turnover",
			"b.floatmv    AS floatmv",
			"b.totalmv    AS totalmv",
		)
	}
	columns = append(columns,
		"b.change_pct AS change_pct",
		"b.amplitude  AS amplitude",
	)
	return strings.Join(columns, ",\n\t\t\t")
}
