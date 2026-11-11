package tdx

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

//go:embed eltdx_online.py
var eltdxOnlineScript string

func runEltdx(ctx context.Context, mode string, args ...string) ([]byte, error) {
	python := os.Getenv("TDX2DB_PYTHON")
	if python == "" {
		python = "python3"
		if runtime.GOOS == "windows" {
			python = "python"
		}
	}

	commandArgs := append([]string{"-", mode}, args...)
	cmd := exec.CommandContext(ctx, python, commandArgs...)
	cmd.Stdin = strings.NewReader(eltdxOnlineScript)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("eltdx %s failed: %w: %s", mode, err, detail)
		}
		return nil, fmt.Errorf("eltdx %s failed: %w", mode, err)
	}
	return output, nil
}
