package workflow

import (
	"fmt"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
)

// WorkPlan 在任务图启动前汇总交易日历与各表最新日期，决定哪些任务真正需要跑。
// 任务框架里的 SkipIf 只读取本结构，不再各自重复查询。
type WorkPlan struct {
	Today          time.Time
	LastTradingDay time.Time
	Calendar       *TradingCalendar

	NeedDaily    bool
	NeedGbbq     bool
	NeedBasic    bool
	NeedFactor   bool
	NeedHolidays bool

	Reason string // 用于日志
}

// AnyNeeded 是否有任何任务需要执行。
func (p *WorkPlan) AnyNeeded() bool {
	return p.NeedDaily || p.NeedGbbq || p.NeedBasic || p.NeedFactor || p.NeedHolidays
}

// BuildWorkPlan 读取交易日历与各表最新日期，推导本次 cron 要做什么。
func BuildWorkPlan(db database.DataRepository, today time.Time) (*WorkPlan, error) {
	holidays, err := db.GetHolidays()
	if err != nil {
		return nil, fmt.Errorf("failed to load holidays: %w", err)
	}

	plan := &WorkPlan{Today: today}

	// raw_holidays 为空：属于首次运行 / 旧库，放行所有任务让其自行写入节假日。
	if len(holidays) == 0 {
		plan.NeedDaily = true
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
		plan.Reason = "🌱 raw_holidays 为空，走完整流程"
		return plan, nil
	}

	cal := NewTradingCalendar(holidays)
	plan.Calendar = cal
	plan.LastTradingDay = cal.LastTradingDayOnOrBefore(today)

	dailyDates, err := db.GetLatestDates(model.TableKlineDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest daily date: %w", err)
	}
	dailyLatest := latestDateInMap(dailyDates)
	if dailyLatest.IsZero() {
		plan.Reason = "🛑 数据库无日线数据，请先运行 init"
		return plan, nil
	}

	basicDates, err := db.GetLatestDates(model.TableBasicDaily.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest basic date: %w", err)
	}
	factorDates, err := db.GetLatestDates(model.TableAdjustFactor.TableName, "date")
	if err != nil {
		return nil, fmt.Errorf("failed to get latest factor date: %w", err)
	}
	symbols, err := db.GetSymbolsByClass(model.ClassStock, model.ClassETF, model.ClassBStock)
	if err != nil {
		return nil, fmt.Errorf("failed to query calculation symbols: %w", err)
	}
	invalidFactors, err := db.HasInvalidFactors()
	if err != nil {
		return nil, err
	}

	plan.NeedDaily = dailyLatest.Before(plan.LastTradingDay)
	// gbbq 与日线同频：日线没新数据时 gbbq 也无须更新。
	plan.NeedGbbq = plan.NeedDaily
	// basic/factor 必须按标判断，避免全库 MAX(date) 掩盖新增或滞后的 bstock。
	plan.NeedBasic = plan.NeedDaily
	plan.NeedFactor = plan.NeedDaily || invalidFactors
	for _, symbol := range symbols {
		dailyDate, hasDaily := dailyDates[symbol]
		basicDate, hasBasic := basicDates[symbol]
		factorDate, hasFactor := factorDates[symbol]

		if !hasBasic || (hasDaily && dateBefore(basicDate, dailyDate)) {
			plan.NeedBasic = true
		}
		if !hasFactor || (hasBasic && dateBefore(factorDate, basicDate)) {
			plan.NeedFactor = true
		}
	}
	// holidays 来自 gbbq.zip，与 gbbq 同频刷新即可。
	plan.NeedHolidays = plan.NeedGbbq

	plan.Reason = describePlan(plan, dailyLatest)
	return plan, nil
}

func latestDateInMap(dates map[string]time.Time) time.Time {
	var latest time.Time
	for _, date := range dates {
		if date.After(latest) {
			latest = date
		}
	}
	return latest
}

func describePlan(plan *WorkPlan, dailyLatest time.Time) string {
	dayStr := plan.Today.Format("2006-01-02")
	cal := plan.Calendar

	if plan.NeedDaily {
		return fmt.Sprintf("📅 数据库日线最新 %s，执行更新",
			dailyLatest.Format("2006-01-02"))
	}

	if plan.NeedBasic || plan.NeedFactor {
		return fmt.Sprintf("📅 日线已是最新 (%s)，补算 basic/factor", dailyLatest.Format("2006-01-02"))
	}

	switch {
	case cal.IsHoliday(plan.Today):
		return fmt.Sprintf("🎉 今日 %s 为节假日，A 股休市，全部任务跳过", dayStr)
	case cal.IsWeekend(plan.Today):
		return fmt.Sprintf("🌴 今日 %s 为周末，A 股休市，全部任务跳过", dayStr)
	default:
		return fmt.Sprintf("✅ 数据库已是最新 (%s)，全部任务跳过", dailyLatest.Format("2006-01-02"))
	}
}
