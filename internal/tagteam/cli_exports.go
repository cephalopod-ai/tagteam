package tagteam

import (
	"os"
	"path/filepath"
)

func ReadLatestForCLI(workdir string) (LatestRun, error) {
	return readLatest(workdir)
}

func ReadPlanForCLI(runDir string) (ExecutionPlan, error) {
	return readExecutionPlan(runDir)
}

func ReadActiveRunForCLI(workdir string) (ActiveRun, error) {
	return readActiveRun(workdir)
}

func UserConfigPathForCLI() (string, error) {
	return userConfigPath()
}

func EnsureGitignoreEntryForCLI(workdir, entry string) error {
	return ensureGitignoreEntry(workdir, entry)
}

func WriteFileDurableForCLI(path string, data []byte, mode os.FileMode) error {
	return writeFileDurable(path, data, mode, true)
}

func RunDirForCLI(workdir, runID string) (string, error) {
	return runDirForWorkdir(workdir, runID)
}

// RunDirForCLIAtStateRoot resolves a run in an explicitly selected
// authoritative state root rather than inferring the repository pointer.
func RunDirForCLIAtStateRoot(workdir, stateRoot, runID string) (string, error) {
	if stateRoot == "" {
		return runDirForWorkdir(workdir, runID)
	}
	locator, err := resolveStateLocator(workdir, stateRoot)
	if err != nil {
		return "", err
	}
	return locator.RunDir(runID)
}

func RunsRootForCLI(workdir string) string {
	if locator, ok := existingStateLocator(workdir); ok {
		return locator.RunsRoot
	}
	return filepath.Join(workdir, ".tagteam", "runs")
}

func CodeIntelRepoAllowedForCLI(workdir string, allowed []string) bool {
	return codeIntelRepoAllowed(workdir, allowed)
}
