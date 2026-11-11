package cmd

import "testing"

func TestResolveBoardDataDir(t *testing.T) {
	t.Setenv("TDX2DB_BOARD_DATA_DIR", `C:\env\board`)
	if got := resolveBoardDataDir(`C:\flag\board`); got != `C:\flag\board` {
		t.Fatalf("flag should win, got %q", got)
	}
	if got := resolveBoardDataDir(""); got != `C:\env\board` {
		t.Fatalf("environment fallback = %q", got)
	}
}
