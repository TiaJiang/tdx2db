package database

import (
	"context"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

type DataRepository interface {
	Connect() error
	Close() error

	InitSchema() error

	ReadSchemaVersion() (string, error)
	WriteSchemaVersion() error

	ImportCSV(meta *model.TableMeta, csvPath string) error
	ImportKlineDaily(csvPath string) error
	ImportKline1Min(csvPath string) error
	ImportAdjustFactors(csvPath string, rebuildSymbols []string) error
	ImportGBBQ(csvPath string) error
	ImportBasic(csvPath string, rebuildSymbols []string) error
	ImportHolidays(csvPath string) error
	ImportBlockInfo(csvPath string) error
	ImportBlockMembers(csvPath string) error
	ImportSymbolNames(csvPath string) error

	TruncateTable(meta *model.TableMeta) error
	Query(table string, conditions map[string]interface{}, dest interface{}) error
	QueryRows(ctx context.Context, query string, dest interface{}) error
	QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error)
	GetLatestDate(tableName string, dateCol string) (time.Time, error)
	GetLatestDates(tableName string, dateCol string) (map[string]time.Time, error)
	GetMinDate(tableName string, dateCol string) (time.Time, error)
	GetSymbolsByClass(classes ...string) ([]string, error)
	RebuildSymbolClass() error
	CountKlineDaily() (int64, error)

	GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error)
	GetLatestBasics(classes ...string) (map[string]model.BasicDaily, error)
	GetKlineDailyAfterLatestBasic(classes ...string) (map[string][]model.KlineDay, error)
	GetLatestFactor(symbol string) (*model.Factor, error)
	GetLatestFactors(classes ...string) (map[string]model.Factor, error)
	GetBasicsAfterLatestFactor(classes ...string) (map[string][]model.BasicDaily, error)
	HasInvalidFactors() (bool, error)
	GetInvalidFactorSymbols() ([]string, error)

	GetGbbq() ([]model.GbbqData, error)
	GetHolidays() ([]time.Time, error)
}
