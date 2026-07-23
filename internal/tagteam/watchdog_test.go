package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSoftProgressMonitorResetsOnOutputArtifactTelemetry(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	runDir := t.TempDir()
	outputPath := filepath.Join(runDir, "provider.json")
	started := time.Now()
	lastActivity := started
	stop := startSoftProgressMonitor(context.Background(), Request{
		Workdir:              repo,
		RunDir:               runDir,
		OutputPath:           outputPath,
		WatchdogTimeout:      100 * time.Millisecond,
		ProgressLastActivity: &lastActivity,
		Quiet:                true,
	}, RoleReporter, "telemetry regression", started, nil)
	defer stop()

	progressPath := filepath.Join(runDir, liveProgressArtifact)
	awaitLiveProgressStatus(t, progressPath, "awaiting_telemetry", 2*time.Second)
	telemetryAt := time.Now()
	if err := os.WriteFile(outputPath, []byte(`{"status":"partial"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	progress := awaitLiveProgressStatusAfter(t, progressPath, "running", telemetryAt, 2*time.Second)
	if !progress.LastActivityAt.After(telemetryAt) {
		t.Fatalf("last activity = %s, want output-artifact telemetry after %s", progress.LastActivityAt, telemetryAt)
	}
}

func awaitLiveProgressStatus(t *testing.T, path, status string, timeout time.Duration) LiveProgress {
	return awaitLiveProgressStatusAfter(t, path, status, time.Time{}, timeout)
}

func awaitLiveProgressStatusAfter(t *testing.T, path, status string, activityAfter time.Time, timeout time.Duration) LiveProgress {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var latest LiveProgress
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &latest); err != nil {
				t.Fatal(err)
			}
			if latest.Status == status && (activityAfter.IsZero() || latest.LastActivityAt.After(activityAfter)) {
				return latest
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("live progress never reached %q: %#v", status, latest)
	return LiveProgress{}
}
