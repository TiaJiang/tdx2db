package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

var TaskUpdateGBBQ *Task

const (
	ExtraGbbqChanged        = "gbbq_changed"
	ExtraGbbqChangedSymbols = "gbbq_changed_symbols"
)

func init() {
	TaskUpdateGBBQ = &Task{
		Name:      "update_gbbq",
		DependsOn: []string{"fetch_gbbq"},
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedGbbq }),
		Executor:  executeUpdateGBBQ,
	}
	registerTask(TaskUpdateGBBQ, "update")
}

func executeUpdateGBBQ(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	gbbqFile := filepath.Join(args.TempDir, "gbbq-temp", "gbbq")

	oldData, err := db.GetGbbq()
	if err != nil {
		return nil, fmt.Errorf("failed to read existing GBBQ: %w", err)
	}

	gbbqData, err := tdx.DecodeGbbqFile(gbbqFile)
	if err != nil {
		return nil, fmt.Errorf("failed to decode GBBQ: %w", err)
	}
	changed, changedSymbols := diffGbbq(oldData, gbbqData)

	if args.Extra == nil {
		args.Extra = map[string]interface{}{}
	}
	args.Extra[ExtraGbbqChanged] = changed
	args.Extra[ExtraGbbqChangedSymbols] = changedSymbols

	if !changed {
		fmt.Println("🌲 股本变迁数据无变化")
		return &TaskResult{State: StateSkipped, Message: "gbbq data unchanged"}, nil
	}

	gbbqCSV := filepath.Join(args.TempDir, "gbbq.csv")
	gbbqCw, err := utils.NewCSVWriter[model.GbbqData](gbbqCSV)
	if err != nil {
		return nil, fmt.Errorf("failed to create GBBQ CSV writer: %w", err)
	}
	if err := gbbqCw.Write(gbbqData); err != nil {
		return nil, err
	}
	gbbqCw.Close()

	if err := db.ImportGBBQ(gbbqCSV); err != nil {
		return nil, fmt.Errorf("failed to import GBBQ csv into database: %w", err)
	}

	fmt.Printf("📈 股本变迁数据导入成功 (changed symbols=%d)\n", len(changedSymbols))
	return &TaskResult{
		State:   StateCompleted,
		Rows:    len(gbbqData),
		Message: fmt.Sprintf("gbbq data imported (changed=%t, symbols=%d)", changed, len(changedSymbols)),
	}, nil
}

func equalGbbq(a, b []model.GbbqData) bool {
	changed, _ := diffGbbq(a, b)
	return !changed
}

func diffGbbq(a, b []model.GbbqData) (bool, []string) {
	left := append([]model.GbbqData(nil), a...)
	right := append([]model.GbbqData(nil), b...)
	less := func(rows []model.GbbqData) func(i, j int) bool {
		return func(i, j int) bool {
			return compareGbbq(rows[i], rows[j]) < 0
		}
	}
	sort.Slice(left, less(left))
	sort.Slice(right, less(right))

	changedSymbols := make(map[string]struct{})
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		switch {
		case i == len(left):
			changedSymbols[right[j].Symbol] = struct{}{}
			j++
		case j == len(right):
			changedSymbols[left[i].Symbol] = struct{}{}
			i++
		default:
			cmp := compareGbbq(left[i], right[j])
			if cmp == 0 {
				i++
				j++
			} else if cmp < 0 {
				changedSymbols[left[i].Symbol] = struct{}{}
				i++
			} else {
				changedSymbols[right[j].Symbol] = struct{}{}
				j++
			}
		}
	}

	symbols := make([]string, 0, len(changedSymbols))
	for symbol := range changedSymbols {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return len(symbols) > 0, symbols
}

func compareGbbq(a, b model.GbbqData) int {
	switch {
	case a.Symbol < b.Symbol:
		return -1
	case a.Symbol > b.Symbol:
		return 1
	}
	aDate := a.Date.Format("2006-01-02")
	bDate := b.Date.Format("2006-01-02")
	switch {
	case aDate < bDate:
		return -1
	case aDate > bDate:
		return 1
	case a.Category < b.Category:
		return -1
	case a.Category > b.Category:
		return 1
	}
	for _, pair := range [][2]float64{
		{a.C1, b.C1},
		{a.C2, b.C2},
		{a.C3, b.C3},
		{a.C4, b.C4},
	} {
		switch {
		case pair[0] < pair[1]:
			return -1
		case pair[0] > pair[1]:
			return 1
		}
	}
	return 0
}
