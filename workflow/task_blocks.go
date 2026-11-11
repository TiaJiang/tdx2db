package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/model"
	"github.com/jing2uo/tdx2db/tdx"
	"github.com/jing2uo/tdx2db/utils"
)

var TaskUpdateBlocks *Task

func init() {
	TaskUpdateBlocks = &Task{
		Name:      "update_blocks",
		DependsOn: []string{"update_daily"},
		Executor:  executeUpdateBlocks,
		OnError:   ErrorModeSkip,
	}
	registerTask(TaskUpdateBlocks, "update")
}

func executeUpdateBlocks(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	fmt.Println("开始更新板块元数据")

	indexSymbols, err := db.GetSymbolsByClass(model.ClassIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to query index symbols: %w", err)
	}

	result, err := tdx.FetchOnlineBlocks(ctx, tdx.BlockFetchOptions{
		BoardDataDir: args.BoardDataDir,
		TempDir:      args.TempDir,
		IndexSymbols: indexSymbols,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block metadata: %w", err)
	}

	var previousBlocks []model.BlockInfo
	previousInfoQuery := `SELECT block_type, block_name, block_symbol, block_code,
		COALESCE(parent_code, '') AS parent_code, block_level FROM ` + model.TableBlockInfo.TableName
	if err := db.QueryRows(ctx, previousInfoQuery, &previousBlocks); err != nil {
		return nil, fmt.Errorf("failed to query previous block info: %w", err)
	}
	var previousMembers []model.BlockMember
	if err := db.QueryRows(ctx, "SELECT * FROM "+model.TableBlockMember.TableName, &previousMembers); err != nil {
		return nil, fmt.Errorf("failed to query previous block members: %w", err)
	}
	result.ApplyPrevious(previousBlocks, previousMembers)

	for _, warning := range result.Warnings {
		fmt.Printf("板块警告: %s\n", warning)
	}
	if len(result.Blocks) == 0 {
		return nil, fmt.Errorf("no block metadata available; previous tables were left unchanged")
	}

	infoCSV := filepath.Join(args.TempDir, "tdx_blocks_info.csv")
	infoWriter, err := utils.NewCSVWriter[model.BlockInfo](infoCSV)
	if err != nil {
		return nil, fmt.Errorf("failed to create block info CSV writer: %w", err)
	}
	if err := infoWriter.Write(result.Blocks); err != nil {
		infoWriter.Close()
		return nil, fmt.Errorf("failed to write block info CSV: %w", err)
	}
	if err := infoWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close block info CSV: %w", err)
	}

	memberCSV := filepath.Join(args.TempDir, "tdx_blocks_member.csv")
	if len(result.Members) == 0 {
		if err := os.WriteFile(memberCSV, []byte("stock_symbol,block_code\n"), 0644); err != nil {
			return nil, fmt.Errorf("failed to create empty block member CSV: %w", err)
		}
	} else {
		memberWriter, err := utils.NewCSVWriter[model.BlockMember](memberCSV)
		if err != nil {
			return nil, fmt.Errorf("failed to create block member CSV writer: %w", err)
		}
		if err := memberWriter.Write(result.Members); err != nil {
			memberWriter.Close()
			return nil, fmt.Errorf("failed to write block member CSV: %w", err)
		}
		if err := memberWriter.Close(); err != nil {
			return nil, fmt.Errorf("failed to close block member CSV: %w", err)
		}
	}

	if err := db.ImportBlockSnapshot(infoCSV, memberCSV); err != nil {
		return nil, fmt.Errorf("failed to import block snapshot: %w", err)
	}

	message := fmt.Sprintf(
		"blocks imported: %d info rows, %d member rows, %d warnings, %d failed upstream blocks",
		len(result.Blocks), len(result.Members), len(result.Warnings), len(result.FailedBlocks),
	)
	fmt.Println("板块数据导入成功")
	return &TaskResult{
		State:   StateCompleted,
		Rows:    len(result.Blocks) + len(result.Members),
		Message: message,
	}, nil
}
