package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartupProcess(t *testing.T) {
	if os.Getenv("ARCAM_TEST_STARTUP") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("arcam", flag.ExitOnError)
	os.Args = []string{"arcam", "-mcp-stdio", "-seed-demo=false", "-addr", "0.0.0.0:8080", "-data", os.Getenv("ARCAM_TEST_DATA")}
	main()
	os.Exit(0)
}

func TestStartupLogsBeforeRecoveryAndKeepsMCPStdoutClean(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "invalid_saved_run"}[corrupt], func(t *testing.T) {
			dir := t.TempDir()
			if corrupt {
				if err := os.Mkdir(filepath.Join(dir, "runs"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "runs", "broken.json"), []byte(`{"status":"running","pairs":[`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartupProcess$")
			cmd.Env = append(os.Environ(), "ARCAM_TEST_STARTUP=1", "ARCAM_TEST_DATA="+dir, "ARCAM_API_TOKEN=startup-test", "ENABLE_LIVE_TRADING=false", "MAX_LIVE_ORDER_NOTIONAL=100")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run() // Closed stdin lets MCP exit after initialization.
			if (err != nil) != corrupt || ctx.Err() != nil {
				t.Fatalf("unexpected startup result: %v\n%s", err, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("startup logs polluted MCP stdout: %s", stdout.String())
			}
			logs := stderr.String()
			begin := strings.Index(logs, "Arcam starting")
			recover := strings.Index(logs, "startup: recovering backtest statuses")
			if begin < 0 || recover <= begin {
				t.Fatal("missing startup progress", logs)
			}
			if corrupt {
				if !strings.Contains(logs, "recover backtests: recover runs/broken:") || strings.Contains(logs, "Arcam ready") {
					t.Fatal("startup error lacks stage/file context", logs)
				}
			} else if !strings.Contains(logs, "Arcam ready over MCP stdio") {
				t.Fatal("missing ready log", logs)
			}
		})
	}
}
