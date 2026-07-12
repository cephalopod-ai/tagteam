package tagteam

import (
	"os"
	"path/filepath"
)

func ReadLatestForCLI(workdir string) (LatestRun, error) {
	return readLatest(workdir)
}

func ReadFinalForCLI(path string) (FinalRun, error) {
	return readFinal(path)
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

func RunsRootForCLI(workdir string) string {
	if locator, ok := existingStateLocator(workdir); ok {
		return locator.RunsRoot
	}
	return filepath.Join(workdir, ".tagteam", "runs")
}

func CodeIntelRepoAllowedForCLI(workdir string, allowed []string) bool {
	return codeIntelRepoAllowed(workdir, allowed)
}
