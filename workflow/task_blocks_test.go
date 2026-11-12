package workflow

import "testing"

func TestUpdateBlocksDependsOnSymbolNames(t *testing.T) {
	dependencies := make(map[string]bool)
	for _, dependency := range TaskUpdateBlocks.DependsOn {
		dependencies[dependency] = true
	}
	if !dependencies["update_symbol_names"] || !dependencies["fetch_gbbq"] {
		t.Fatalf("update_blocks dependencies = %v, want update_symbol_names and fetch_gbbq", TaskUpdateBlocks.DependsOn)
	}
}
