package tagteam

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// configuredConfigTestCommands keeps the legacy singular key working while
// treating the explicit list as authoritative for concurrent execution.
func configuredConfigTestCommands(legacy string, commands []string) []string {
	if commands != nil {
		return append([]string(nil), commands...)
	}
	if strings.TrimSpace(legacy) == "" {
		return nil
	}
	return []string{legacy}
}

func configuredPresetTestCommands(preset TestPresetConfig) []string {
	return configuredConfigTestCommands(preset.Command, preset.Commands)
}

func configuredTestCommands(opts RunOptions) []string {
	return configuredConfigTestCommands(opts.TestCmd, opts.TestCommands)
}

func hasConfiguredTests(opts RunOptions) bool {
	return len(configuredTestCommands(opts)) > 0
}

func testCommandDescription(opts RunOptions) string {
	commands := configuredTestCommands(opts)
	switch len(commands) {
	case 0:
		return ""
	case 1:
		return commands[0]
	default:
		return fmt.Sprintf("%d independent commands (parallel)", len(commands))
	}
}

func validateConfiguredTestCommands(opts RunOptions) error {
	return validateTestCommands(opts.Workdir, configuredTestCommands(opts))
}

func validateTestCommands(workdir string, commands []string) error {
	if len(commands) == 0 {
		return nil
	}
	for index, command := range commands {
		if strings.TrimSpace(command) == "" {
			return &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("test command %d is empty", index+1)}
		}
		if err := validateTestCommand(workdir, command); err != nil {
			return fmt.Errorf("test command %d: %w", index+1, err)
		}
	}
	return nil
}

func runConfiguredTestCommands(ctx context.Context, opts RunOptions, outputPath string) (TestRun, error) {
	return runTestCommands(ctx, opts.Workdir, configuredTestCommands(opts), opts.Timeout, outputPath, opts.DryRun, opts.EnvOverlay, opts.MaxOutputBytes, opts.TestIdentityRegex)
}

// runTestCommands runs an explicitly configured set of independent test
// commands concurrently. It waits for every command, then writes ordered,
// aggregated evidence to outputPath. Individual commands retain distinct
// isolation directories and output files to avoid shared state collisions.
func runTestCommands(ctx context.Context, workdir string, commands []string, timeout time.Duration, outputPath string, dryRun bool, envOverlay map[string]string, maxBytes int64, identityRegex string) (TestRun, error) {
	commands = append([]string(nil), commands...)
	if len(commands) == 0 {
		return TestRun{}, fmt.Errorf("no test commands configured")
	}
	if err := validateTestCommands(workdir, commands); err != nil {
		return TestRun{}, err
	}
	if len(commands) == 1 {
		return runTestCommand(ctx, workdir, commands[0], timeout, outputPath, dryRun, envOverlay, maxBytes, identityRegex)
	}

	type commandResult struct {
		index      int
		outputPath string
		run        TestRun
		err        error
	}
	results := make(chan commandResult, len(commands))
	var wait sync.WaitGroup
	for index, command := range commands {
		wait.Add(1)
		go func(index int, command string) {
			defer wait.Done()
			childOutputPath := parallelTestOutputPath(outputPath, index)
			run, err := runTestCommand(ctx, workdir, command, timeout, childOutputPath, dryRun, envOverlay, maxBytes, identityRegex)
			results <- commandResult{index: index, outputPath: childOutputPath, run: run, err: err}
		}(index, command)
	}
	wait.Wait()
	close(results)

	ordered := make([]commandResult, len(commands))
	for result := range results {
		ordered[result.index] = result
	}

	aggregate := TestRun{
		Command:  fmt.Sprintf("parallel (%d commands)", len(commands)),
		Passed:   true,
		Commands: make([]TestCommandRun, len(commands)),
	}
	output := newBoundedBuffer(maxBytes)
	failures := map[string]bool{}
	var firstErr error
	for index, result := range ordered {
		child := result.run
		aggregate.Commands[index] = TestCommandRun{
			Command:           commands[index],
			Output:            child.Output,
			Passed:            child.Passed && result.err == nil,
			ExitCode:          child.ExitCode,
			FailureIdentities: append([]string(nil), child.FailureIdentities...),
			StateRoot:         child.StateRoot,
			TempDir:           child.TempDir,
			OutputPath:        result.outputPath,
		}
		if !child.Passed || result.err != nil {
			aggregate.Passed = false
		}
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("parallel test command %d: %w", index+1, result.err)
		}
		for _, identity := range child.FailureIdentities {
			failures[identity] = true
		}
		appendParallelTestOutput(output, index, len(commands), commands[index], child.Output, result.err, envOverlay)
	}
	if output.Exceeded() {
		aggregate.Passed = false
	}
	for identity := range failures {
		aggregate.FailureIdentities = append(aggregate.FailureIdentities, identity)
	}
	sort.Strings(aggregate.FailureIdentities)
	aggregate.Output = redactSecretsWithOverlay(output.String(), envOverlay)
	if !dryRun {
		if err := writeRedactedBytes(outputPath, output.Bytes(), envOverlay); err != nil {
			return TestRun{}, fmt.Errorf("persist parallel test output: %w", err)
		}
	}
	if firstErr != nil {
		return TestRun{}, firstErr
	}
	return aggregate, nil
}

func parallelTestOutputPath(outputPath string, index int) string {
	extension := filepath.Ext(outputPath)
	base := strings.TrimSuffix(outputPath, extension)
	return fmt.Sprintf("%s-%d%s", base, index+1, extension)
}

func appendParallelTestOutput(output *boundedBuffer, index, total int, command, childOutput string, runErr error, envOverlay map[string]string) {
	_, _ = fmt.Fprintf(output, "==> test %d/%d: %s\n", index+1, total, command)
	if childOutput != "" {
		_, _ = output.Write([]byte(childOutput))
		if !strings.HasSuffix(childOutput, "\n") {
			_, _ = output.Write([]byte("\n"))
		}
	}
	if runErr != nil {
		_, _ = fmt.Fprintf(output, "tagteam: %s\n", redactSecretsWithOverlay(runErr.Error(), envOverlay))
	}
}
