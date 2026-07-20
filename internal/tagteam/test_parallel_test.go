package tagteam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunTestCommandsRunsExplicitCommandsConcurrently(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "started")
	outputPath := filepath.Join(root, "parallel-tests.txt")
	commands := []string{
		parallelBarrierCommand(marker, "first"),
		parallelBarrierCommand(marker, "second"),
	}

	run, err := runTestCommands(context.Background(), root, commands, 3*time.Second, outputPath, false, nil, 0, "")
	if err != nil {
		t.Fatalf("runTestCommands() error = %v", err)
	}
	if !run.Passed || len(run.Commands) != 2 {
		t.Fatalf("parallel test run = %#v", run)
	}
	if run.Command != "parallel (2 commands)" {
		t.Fatalf("aggregate command = %q", run.Command)
	}
	if run.Commands[0].StateRoot == run.Commands[1].StateRoot || run.Commands[0].TempDir == run.Commands[1].TempDir {
		t.Fatalf("parallel tests reused isolation directories: %#v", run.Commands)
	}
	for index, command := range run.Commands {
		if command.Command != commands[index] || command.OutputPath == "" {
			t.Fatalf("command[%d] = %#v", index, command)
		}
		if _, err := os.Stat(command.OutputPath); err != nil {
			t.Fatalf("command[%d] output path %q: %v", index, command.OutputPath, err)
		}
	}
	started, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(started))
	sort.Strings(lines)
	if !reflect.DeepEqual(lines, []string{"first", "second"}) {
		t.Fatalf("barrier starts = %#v", lines)
	}
	aggregate, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aggregate), "==> test 1/2:") || !strings.Contains(string(aggregate), "==> test 2/2:") {
		t.Fatalf("aggregate output missing command headings: %s", aggregate)
	}
}

func TestRunTestCommandsValidatesAllCommandsBeforeLaunching(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "launched")
	writeMarker := "sh -c " + strconv.Quote(`printf launched > "$1"`) + " _ " + strconv.Quote(marker)
	_, err := runTestCommands(context.Background(), root, []string{writeMarker, "go test ./missing_test.go"}, time.Second, filepath.Join(root, "parallel-tests.txt"), false, nil, 0, "")
	if err == nil || !strings.Contains(err.Error(), "test command 2") {
		t.Fatalf("runTestCommands() error = %v, want second-command preflight failure", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("valid command launched before complete preflight: stat error = %v", statErr)
	}
}

func TestResolveOptionsUsesParallelTestCommands(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Defaults.Tests = []string{"go test ./one", "go test ./two"}
	opts, err := ResolveOptions(cfg, nil, FlagInputs{Timeout: 15 * time.Minute}, nil, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() error = %v", err)
	}
	if opts.TestCmd != "go test ./one" || !reflect.DeepEqual(opts.TestCommands, cfg.Defaults.Tests) {
		t.Fatalf("resolved config tests = command:%q commands:%#v", opts.TestCmd, opts.TestCommands)
	}

	opts, err = ResolveOptions(cfg, nil, FlagInputs{Tests: []string{"go test ./three", "go test ./four"}, Timeout: 15 * time.Minute}, map[string]bool{"test": true}, "ship it")
	if err != nil {
		t.Fatalf("ResolveOptions() CLI override error = %v", err)
	}
	if opts.TestCmd != "go test ./three" || !reflect.DeepEqual(opts.TestCommands, []string{"go test ./three", "go test ./four"}) {
		t.Fatalf("resolved CLI tests = command:%q commands:%#v", opts.TestCmd, opts.TestCommands)
	}
}

func parallelBarrierCommand(marker, label string) string {
	script := fmt.Sprintf(`printf "%%s\n" %s >> "$1"; while [ "$(wc -l < "$1")" -lt 2 ]; do sleep 0.01; done; printf "%%s\n" %s`, strconv.Quote(label), strconv.Quote(label))
	return "sh -c '" + script + "' _ " + strconv.Quote(marker)
}
