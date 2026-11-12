package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/jing2uo/tdx2db/database"
)

func TestExecutorContinuesAfterErrorModeSkipDependency(t *testing.T) {
	dependentRan := false
	tasks := map[string]*Task{
		"optional": {
			Name:    "optional",
			OnError: ErrorModeSkip,
			Executor: func(context.Context, database.DataRepository, *TaskArgs) (*TaskResult, error) {
				return nil, errors.New("upstream unavailable")
			},
		},
		"dependent": {
			Name:      "dependent",
			DependsOn: []string{"optional"},
			Executor: func(context.Context, database.DataRepository, *TaskArgs) (*TaskResult, error) {
				dependentRan = true
				return &TaskResult{State: StateCompleted}, nil
			},
		},
	}

	executor := NewTaskExecutor(nil, tasks)
	if err := executor.Run(context.Background(), []string{"optional", "dependent"}, &TaskArgs{}); err != nil {
		t.Fatal(err)
	}
	if !dependentRan {
		t.Fatal("dependent task did not run after an ErrorModeSkip dependency failed")
	}
}
