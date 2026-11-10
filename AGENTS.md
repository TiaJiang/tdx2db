# PROJECT KNOWLEDGE BASE

**Updated:** 2026-05-15
**Branch:** feat/opentdx-online

## OVERVIEW
通达信(TDX) stock data importer — loads .day/.01/.5 files to DuckDB/ClickHouse, pulls online code names + block/industry/concept data via the eltdx Python client, calculates preclose/turnover/market-value (basic), and 后复权因子 (hfq_factor). basic/factor 链路覆盖 stock + ETF + B 股 (ETF 含 LOF/老封基)。

Current schema version: **v7.0** (`model.SchemaMajor=7, SchemaMinor=0`). The `_meta` table stores the schema version; init/cron check major version compatibility at startup.

## STRUCTURE
```
./
├── calc/       # Financial calculations (basic indicators + 复权因子)
│   ├── basic.go           # preclose, turnover, floatmv, totalmv
│   ├── fq_quantaxis.go    # HFQ factor (QUANTAXIS-based)
│   └── fq_quantaxis_test.go
├── cmd/        # CLI commands (init, cron)
├── database/   # DB interface + implementations (duckdb/clickhouse)
├── model/      # Data models, table registry, view registry
├── tdx/        # TDX binary format parsing
├── utils/      # Utilities (cache, pipeline, CSV, download)
├── workflow/   # Task execution framework (DAG, calendar, work plan)
└── main.go     # Cobra CLI entry point
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add new database backend | ./database/ | Implement DataRepository interface |
| Parse new TDX format | ./tdx/ | Binary format parsers (day, minline, gbbq, holidays) + online eltdx client data |
| Modify basic calculation | ./calc/basic.go | preclose / turnover / market-value |
| Modify HFQ factor | ./calc/fq_quantaxis.go | 后复权因子算法 |
| Add CLI command | ./cmd/ + main.go | Cobra subcommand with ctx cancel support |
| Data model changes | ./model/ | Schema + struct tags + table/view registry |
| Database queries | ./database/*/dml.go | DB-specific query implementations |
| Add/modify workflow task | ./workflow/task_*.go | Define task with dependencies; each task self-registers in init() |
| Run specific tasks | ./workflow/engine.go | Use TaskExecutor with task names |

## CODE MAP
| Symbol | Type | Location | Role |
|--------|------|----------|------|
| main | func | main.go:37 | Cobra root + ctx setup |
| DataRepository | interface | database/interface.go:9 | DB abstraction (Connect/Import/Query) |
| NewDB | func | database/factory.go:11 | Driver factory (duckdb/clickhouse) |
| Task | type | workflow/engine.go:46 | Task definition with dependencies |
| TaskExecutor | type | workflow/engine.go:66 | DAG-based task execution |
| Init | func | cmd/init.go | Full import via workflow |
| Cron | func | cmd/cron.go:11 | Incremental update via workflow |
| CalculateBasicDaily | func | calc/basic.go:115 | Core basic calculation (preclose/turnover/MV, stock+ETF+B股) |
| calculateFullHfq | func | calc/fq_quantaxis.go:86 | Core HFQ factor calculation |
| BuildWorkPlan | func | workflow/plan.go:34 | 读 holidays + 各表最新日期，决定哪些任务要跑 |
| TradingCalendar | type | workflow/calendar.go:5 | 节假日/周末判定 + 最近交易日查找 |
| KlineDay | type | model/schema.go:14 | Raw daily OHLCV |
| BasicDaily | type | model/schema.go:49 | Calculated basic (preclose/turnover/MV, stock+ETF+B股) |
| Factor | type | model/schema.go:41 | Adjust factor (hfq_factor) |
| GbbqData | type | model/schema.go:61 | 股本变迁 data (cat 1=除权, 11=ETF份额折算, 2/3/5/7/8/9/10=股本变动) |
| BlockInfo | type | model/schema.go:75 | 在线板块/概念/行业信息 |
| BlockMember | type | model/schema.go:84 | 在线板块成分关系 |
| SymbolName | type | model/schema.go:89 | 在线代码名称 (symbol/name/class) |
| ClassifyCode | func | model/classify.go:58 | symbol → stock/index/etf/block/unknown |
| PriceScale | func | model/classify.go:89 | TDX 原始整数价格换算 (股票=100, ETF/B股=1000, 可转债=10000) |
| SchemaFromStruct | func | model/tables.go:54 | Reflect-based table registration |
| FetchOnlineBlocks | func | tdx/block.go:32 | 通过 eltdx 拉取在线板块/概念/行业及成分数据 |
| FetchOnlineSymbolNames | func | tdx/stock.go:22 | 通过 eltdx 拉取沪深京在线代码名称 |

## CONVENTIONS

**Database URI format:**
- ClickHouse: `clickhouse://[user[:password]@][host][:port][/database][?http_port=8123]`
- DuckDB: `duckdb://[path]`

**Table naming:**
- `raw_*` — raw imported data (raw_kline_daily, raw_kline_1min, raw_basic_daily, raw_adjust_factor, raw_gbbq, raw_symbol_class, raw_holidays, raw_tdx_blocks_info, raw_tdx_blocks_member, raw_symbol_name)
- `v_*` — views，共 7 个：v_{stock,etf,bstock}_{qfq,hfq} 6 个复权视图 + `v_index` 原始指数日线；stock/指数价格 ROUND 2 位，ETF/B股 3 位；ETF 视图不含 turnover/floatmv/totalmv
- `_meta` — schema version metadata (key/value store)

**Table registration:**
- All tables auto-registered via `SchemaFromStruct()` init-time calls in `model/tables.go`
- Views registered via `DefineView()` in `model/views.go`
- Use `model.Table*` / `model.MetaTable` constants for table references (never hardcode table names)

**TDX file collection (cmd/common.go):**
- All .day/.01 files collected by suffix, filtered only by `^(sh|sz|bj)\d+$` regex
- No prefix whitelist — full ingest of everything TDX provides
- Symbol classification via `raw_symbol_class` table (rebuilt after each daily import)

**Online eltdx data:**
- `tdx/eltdx.go` runs the embedded `tdx/eltdx_online.py` bridge and decodes its JSON output
- `tdx/eltdx_online.py` uses `TdxClient.codes.all_markets()` for online code names and `MacClient.get_board_list()/get_board_members()` for block/industry/concept data
- `tdx/block.go` / `tdx/stock.go` only handle model filtering, deduplication and sorting
- `update_blocks` and `update_symbol_names` are independent update tasks; they truncate + reimport their target tables

**Symbol classification (model/classify.go):**
- `ClassifyCode(symbol) → stock/index/etf/block/unknown`
- Rules match by (market, numeric prefix) — longest prefix wins
- basic/factor calculation uses `GetSymbolsByClass("stock", "etf", "bstock")` — index/block 仍排除在 calc 输出之外
- stock/etf/bstock 复权视图通过 `raw_symbol_class` 过滤；`v_index` 单独过滤 index，只读 `raw_kline_daily`，不提供指数 qfq/hfq
- B 股 (sh900/sz20) 归为 bstock；ETF/LOF 归为 etf
- `PriceScale(symbol)`：股票/指数/板块 价格单位 0.01 元(=100)，ETF/LOF/B股 0.001 元(=1000)，可转债 0.0001 元(=10000)
- `SymbolFromCode(code)`：6 位裸数字反查市场前缀（考虑 stock/etf/bstock）
- Class `cbond` 包括可转债；Class `unknown` 包括国债 (sh24xxxx) 等未覆盖品种

**Schema versioning (cmd/schema_version.go):**
- `model.SchemaMajor` / `model.SchemaMinor` define current version
- DB stores version in `_meta` table via `ReadSchemaVersion()` / `WriteSchemaVersion()`
- `init`: auto-writes version on fresh DB; rejects if existing major doesn't match
- `cron`: rejects if version missing or major doesn't match
- Breaking changes (table rename, field semantics) → increment `SchemaMajor`

**GbbqData categories:**
- Category 1: 除权除息 (dividends/bonus shares) — C1=分红, C2=配股, C3=送转股, C4=配股价
- Category 11: ETF/LOF 份额折算 — C3=折算系数 (拆分日 PreClose 需除以该系数；多次折算同日累乘)
- Category 2/3/5/7/8/9/10: 股本变动 — C1=变动前流通, C2=变动前总, C3=变动后流通, C4=变动后总
- Stock units in gbbq are 万股 (×10000 = actual shares)

**Error handling:**
- Wrap errors with `%w` for error chain
- Context cancellation returns `ctx.Err()` directly
- CLI exits 0 on context cancel, 1 on error

**CLI pattern:**
- Cobra for commands + flags
- Context passed to all long-running ops
- Signal handler (SIGINT/SIGTERM) → ctx cancel → safe exit
- Temp dir: `$TMPDIR/tdx2db-temp-*`

## CALCULATION LOGIC

### calc/basic.go — CalculateBasicDaily
Input: `[]KlineDay` (raw daily) + `[]GbbqData` → Output: `[]*BasicDaily`

1. **xdxr date mapping** (category=1, 11): gbbq date → KlineDay 索引 via `sort.Search` (handles non-trading days)
2. **ETF cat=11 split**: `mergeSplitFromGbbq` 累乘 Splitc3，PreClose 末尾再除一次
3. **Shares tracking** (category 2/3/5/7/8/9/10): maintains running float/total share counts (ETF 一般无此类记录)
4. **Initial share backfill**: if first gbbq share record is after IPO date, uses its C1/C2 (pre-change values) to backfill the gap
5. **Per-day**: preclose (adjusted for xdxr/split), change_pct, amplitude, turnover (vol/float_shares), floatmv, totalmv

**PreClose formula** (with xdxr + split):
```
preclose = ((prevClose×10 - 分红 + 配股×配股价) / (10 + 配股 + 送转股)) / Splitc3
```
Splitc3 默认 1（无 cat=11）；cat=1 与 cat=11 同日时两者都生效。

### calc/fq_quantaxis.go — calculateFullHfq
Input: `[]BasicDaily` + `[]GbbqData` → Output: `[]Factor`

1. 直接消费 BasicDaily 中已计算好的 PreClose（不再单独 buildXdxrDateSet）
2. **Factor accumulation**: starts at 1.0, on xdxr days: `hfq *= prevClose / preclose`
3. Factor only changes when ratio ≠ 1.0 (uses 1e-9 tolerance)

**Critical invariant**: basic.go 是 PreClose 唯一计算源。fq_quantaxis.go 直接读 BasicDaily.PreClose，所以 basic.go 的日期映射 + cat=1/cat=11 处理是单一事实源，不需要在 fq 里重复。

## ANTI-PATTERNS (THIS PROJECT)

**DO NOT:**
- Remove `context.CancelFunc` defer — required for signal handling
- Ignore `ctx.Done()` checks in loops — prevents graceful shutdown
- Use hardcoded paths — all paths use TempDir cache
- Use raw gbbq dates for matching against trading days — always map via sort.Search
- Assume gbbq dates are trading days — they can fall on weekends/holidays

**NEVER:**
- Commit `tdx/embed/datatool` to git — downloaded at build time
- Import `_ "github.com/duckdb/duckdb-go/v2"` outside init package — register driver early
- 在 fq_quantaxis 里再单独做 xdxr 日期映射 — 必须复用 basic.go 写出的 PreClose
- Put version-check logic in the DB layer — DB only does Read/Write; judgment stays in cmd/

## UNIQUE STYLES

**Task-based workflow (workflow/):**
- `TaskExecutor` (engine.go) manages 任务执行 + DAG 拓扑排序；ready tasks 持续并发调度，失败时 cancel run context 并等待已启动任务收尾
- `WorkPlan` (plan.go) 在任务图启动前汇总日历 + 各表最新日期，决定 NeedDaily/NeedGbbq/NeedBasic/NeedFactor/NeedHolidays；任务的 `SkipIf` 只读 plan，不再各自查询数据库
- `TradingCalendar` (calendar.go) 用 raw_holidays + 周末规则识别交易日，下载日线/分时遇到 404 时区分"节假日跳过"/"数据未发布"
- Tasks split by concern into `workflow/task_*.go` files; each task calls `registerTask()` in init with explicit `DependsOn`
- Parallel execution of ready tasks whose selected in-graph dependencies are completed/skipped; dependencies outside the requested task set are ignored
- Optional tasks via `SkipIf` condition (e.g., `--min`)
- Error modes: `ErrorModeStop` (default) vs `ErrorModeSkip`

**Incremental update logic:**
- `cron` command 先 `BuildWorkPlan(db, today)` → 全 `Skip` 则直接退出，否则跑 DAG: `update_daily → fetch_gbbq → update_gbbq → calc_basic → calc_factor`；`update_holidays` depends on `fetch_gbbq`，`update_blocks` / `update_symbol_names` 独立并行
- raw_holidays 为空（首次/旧库）时 plan 强制走全流程，让 holidays 自行写入
- `BuildWorkPlan` 按 eligible 标的逐个比较 kline/basic/factor 最新日期，新增或滞后的 B 股不会因全库 MAX(date) 已最新而被跳过
- `calc_basic` 无 GBBQ 变化时从各标的最新 basic 日期继续增量追加；GBBQ 变化只重建受影响标的，无法确定影响范围时才全量重算
- `calc_factor` 默认从各标的最新因子日期继续追加，仅当受影响标的 GBBQ 变化或检测到非法因子时按标的重建
- 1min 增量导入由 `--min` 控制（可选任务）

**CSV pipeline pattern:**
- All calculation exports use `utils.Pipeline[I,O]` for concurrent per-symbol processing
- TDX files → convert to CSV → temp dir → DB import
- Temp dir cleaned up on `cobra.OnFinalize`

## COMMANDS
```bash
# Build (downloads datatool)
make build

# Install
make user-install    # ~/.local/bin
make sudo-install    # /usr/local/bin

# Clean
make clean

# GoReleaser release
goreleaser release --clean

# Daily update (with ClickHouse)
tdx2db cron --dburi 'clickhouse://localhost'

# Full init from TDX day files
tdx2db init --dburi 'clickhouse://localhost' --dayfiledir /path/to/vipdoc/

# Online metadata runtime dependency (Python >= 3.10)
python3 -m pip install "eltdx==3.3.5"  # Windows: python -m pip
```

## DEVELOPMENT VERIFICATION PIPELINE

任何代码修改在声明完成前都必须自行验证，并在最终回复中列出实际执行的命令、结果和未验证项。禁止只做静态阅读后声称完成，禁止用 stub 测试替代最终的真实 CGO 测试。

**1. 格式和静态检查**

Windows PowerShell:
```powershell
$env:PATH='\path\to\mingw64\bin;' + $env:PATH
$env:CGO_ENABLED='1'
$env:CC='gcc'
$env:CXX='g++'

gofmt -w <changed-go-files>
git diff --check
go vet ./...
```

Linux:
```bash
export CGO_ENABLED=1
gofmt -w <changed-go-files>
git diff --check
go vet ./...
```

**2. 真实 CGO 单元测试**

Windows 本地使用已安装的 WinLibs：
```powershell
go test -count=1 ./...
```

Linux:
```bash
CGO_ENABLED=1 go test -count=1 ./...
```

**3. 构建验证**

Windows:
```powershell
go build -trimpath -o tdx2db.exe .
```

Linux:
```bash
go build -trimpath -o tdx2db .
```

**4. 流水线集成验证**

涉及 parser、database、calc、model、workflow、schema 或 cron/init 的改动，必须在临时 DuckDB 库上走完整链路，不能直接拿生产库做测试：

Windows PowerShell:
```powershell
.\tdx2db.exe init --dburi 'duckdb://./.tmp/test.db' --dayfiledir \path\to\vipdoc
.\tdx2db.exe cron --dburi 'duckdb://./.tmp/test.db'
.\tdx2db.exe cron --dburi 'duckdb://./.tmp/test.db'
```

Linux:
```bash
./tdx2db init --dburi 'duckdb://./.tmp/test.db' --dayfiledir /path/to/vipdoc
./tdx2db cron --dburi 'duckdb://./.tmp/test.db'
./tdx2db cron --dburi 'duckdb://./.tmp/test.db'
```

`init` 只导入 `--dayfiledir` 下的日线；第一次 `cron` 负责补齐 GBBQ、holidays，并计算 basic/factor。

测试完成后至少检查：
- schema 版本正确。
- raw_kline_daily、raw_basic_daily、raw_adjust_factor 无重复 `(symbol,date)`。
- basic/factor 每个标的覆盖和最新日期一致。
- factor 无非正、NaN、Inf。
- 可转债价格标尺正确。
- 连续两次 cron 无新增数据时不重复全量计算、不产生重复行。
- 临时库验证后清理，不覆盖 `\path\to\tdx.db`，除非用户明确要求部署。

**5. validate 命令**

`tdx2db validate` 实现后，流水线改动必须额外执行：

Windows PowerShell:
```powershell
.\tdx2db.exe validate --dburi 'duckdb://./.tmp/test.db'
```

Linux:
```bash
./tdx2db validate --dburi 'duckdb://./.tmp/test.db'
```

critical 必须为零；历史 OHLC warning 可以存在，但必须在最终结果中报告数量和影响范围。

**6. 部署**

只有用户明确要求时才覆盖 `\path\to\tdx2db.exe` 或 `\path\to\tdx.db`。部署后必须运行 `tdx2db version`，并对实际部署库执行最终校验。

**7. 最终汇报**

最终回复必须包含：
- 修改内容和行为变化。
- `go test`、`go vet`、构建结果。
- 临时库 init/cron/幂等验证结果。
- 关键数据统计和剩余风险。
- 未执行的测试及原因。

## NOTES

**Gotchas:**
- 复权因子算法 based on QUANTAXIS — verify before modifying
- 分时数据无历史 — need to backfill manually
- Symbol code changes not handled (历史记录不更新)
- 指数/板块 (sh000xxx, sz399xxx, sh880/881xxx) 不在 calc 输出范围内（GetSymbolsByClass 只取 stock + etf + bstock）；指数日线通过 `v_index` 提供
- ETF/LOF/B股 K线价格按 0.001 元解析 (`PriceScale`)，否则有 10x 偏差；新增品种前缀时务必检查
- ETF 拆分日 (cat=11) PreClose 必须除以 Splitc3，否则当天涨跌幅会异常
- `raw_symbol_class` is rebuilt from `raw_kline_daily` on each daily import — adding classification rules retroactively will auto-classify on next import
- `raw_symbol_name` comes from online quote-server code-name data and is separate from `raw_symbol_class`; class is stored from the online list but basic/factor still use `raw_symbol_class`
- `raw_tdx_blocks_info` / `raw_tdx_blocks_member` come from online eltdx board data and are not part of basic/factor calculation
- online metadata requires Python >= 3.10 with `eltdx` installed; `TDX2DB_PYTHON` can point to a specific interpreter
- holidays 来自 gbbq.zip 内嵌的 zhb.zip (needini.dat)，不再依赖 tdxhome 安装目录
