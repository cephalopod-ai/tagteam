package tagteam

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	liveProgressArtifact        = "live-progress.json"
	hostActivityArtifact        = "host-activity.json"
	preexistingWorktreeArtifact = "preexisting-worktree.json"
	liveProgressCaptureTimeout  = 2 * time.Second
)

// HostActivity is host-owned telemetry for work that does not run through an
// adapter. It keeps status useful during preflight tests and preserves exact
// attribution when a host command mutates the repository.
type HostActivity struct {
	SchemaVersion int       `json:"schema_version"`
	Actor         string    `json:"actor"`
	Phase         string    `json:"phase"`
	Status        string    `json:"status"`
	Command       string    `json:"command,omitempty"`
	OutputPath    string    `json:"output_path,omitempty"`
	Elapsed       string    `json:"elapsed,omitempty"`
	ChangedFiles  []string  `json:"changed_files,omitempty"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func writeHostActivity(runDir string, activity HostActivity) error {
	if runDir == "" {
		return nil
	}
	activity.SchemaVersion = ArtifactSchemaVersion
	activity.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(filepath.Join(runDir, hostActivityArtifact), activity)
}

type PreexistingWorktree struct {
	SchemaVersion int       `json:"schema_version"`
	Baseline      string    `json:"baseline"`
	Files         []string  `json:"files"`
	CapturedAt    time.Time `json:"captured_at"`
}

// LiveProgress is host-owned runtime evidence collected while an adapter is
// active. Agents never write this artifact.
type LiveProgress struct {
	SchemaVersion  int       `json:"schema_version"`
	InvocationID   string    `json:"invocation_id,omitempty"`
	Phase          string    `json:"phase"`
	Role           Role      `json:"role"`
	Status         string    `json:"status"`
	WaitingFor     string    `json:"waiting_for,omitempty"`
	HolderPID      int       `json:"holder_pid,omitempty"`
	Elapsed        string    `json:"elapsed"`
	FilesChanged   int       `json:"files_changed"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   []string  `json:"changed_files,omitempty"`
	DiffHash       string    `json:"diff_hash,omitempty"`
	StdoutBytes    int64     `json:"stdout_bytes,omitempty"`
	StderrBytes    int64     `json:"stderr_bytes,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
	NoProgressFor  string    `json:"no_progress_for,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func writeWaitingProgress(req Request, role Role, phase string, started time.Time, adapterID string, holderPID int) error {
	if req.RunDir == "" {
		return nil
	}
	return writeJSONAtomic(filepath.Join(req.RunDir, liveProgressArtifact), LiveProgress{
		SchemaVersion: ArtifactSchemaVersion,
		InvocationID:  req.InvocationID,
		Phase:         phase,
		Role:          role,
		Status:        "waiting",
		WaitingFor:    adapterID,
		HolderPID:     holderPID,
		Elapsed:       shortDuration(time.Since(started)),
		UpdatedAt:     time.Now().UTC(),
	})
}

func writeLiveProgress(
	ctx context.Context,
	req Request,
	role Role,
	phase string,
	started time.Time,
	status string,
) (LiveProgress, error) {
	progress := LiveProgress{
		SchemaVersion: ArtifactSchemaVersion,
		InvocationID:  req.InvocationID,
		Phase:         phase,
		Role:          role,
		Status:        status,
		Elapsed:       shortDuration(time.Since(started)),
		UpdatedAt:     time.Now().UTC(),
	}
	if req.ProgressStdout != nil {
		progress.StdoutBytes = req.ProgressStdout.Received()
	}
	if req.ProgressStderr != nil {
		progress.StderrBytes = req.ProgressStderr.Received()
	}
	if req.ProgressLastActivity != nil {
		progress.LastActivityAt = *req.ProgressLastActivity
		progress.NoProgressFor = shortDuration(time.Since(*req.ProgressLastActivity))
	}
	var captureErr error
	if req.Workdir != "" {
		captureCtx, cancel := context.WithTimeout(ctx, liveProgressCaptureTimeout)
		diff, err := captureLiveDiff(captureCtx, req.Workdir)
		cancel()
		captureErr = err
		if err == nil {
			progress.ChangedFiles = diff.files
			progress.FilesChanged = len(diff.files)
			progress.Additions = diff.additions
			progress.Deletions = diff.deletions
			progress.DiffHash = sha256Sum(diff.patch)
		}
	}
	if req.RunDir == "" {
		return progress, captureErr
	}
	if err := writeJSONAtomic(filepath.Join(req.RunDir, liveProgressArtifact), progress); err != nil {
		return progress, err
	}
	return progress, captureErr
}

type liveDiff struct {
	patch     []byte
	files     []string
	additions int
	deletions int
}

func captureLiveDiff(ctx context.Context, workdir string) (liveDiff, error) {
	snapshot, err := captureWorktreeSnapshot(ctx, workdir)
	if err != nil {
		return liveDiff{}, err
	}
	numstatZ, err := runGitCommandBytes(ctx, workdir, []string{"LC_ALL=C"}, "diff", "--numstat", "-z", "HEAD", "--", ".", ":(exclude).tagteam")
	if err != nil {
		return liveDiff{}, err
	}
	diff := liveDiff{files: make([]string, 0, len(snapshot))}
	for _, stat := range parseNumstatZ(numstatZ) {
		diff.additions += stat.Additions
		diff.deletions += stat.Deletions
	}
	for path := range snapshot {
		diff.files = append(diff.files, path)
	}
	sort.Strings(diff.files)

	var fingerprint bytes.Buffer
	for _, path := range diff.files {
		state := snapshot[path]
		fingerprint.WriteString(path)
		fingerprint.WriteByte(0)
		fingerprint.WriteString(state)
		fingerprint.WriteByte(0)
		if !strings.HasPrefix(state, "??:") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(path)))
		if readErr != nil {
			return liveDiff{}, readErr
		}
		diff.additions += textLineCount(data)
	}
	diff.patch = fingerprint.Bytes()
	return diff, nil
}

func textLineCount(data []byte) int {
	if len(data) == 0 || bytes.IndexByte(data, 0) >= 0 {
		return 0
	}
	lines := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}

func writeJSONAtomic(path string, value any) error {
	return writeJSONDurable(path, value, true, true)
}

func writePreexistingWorktree(ctx context.Context, workdir, runDir, baseline string) error {
	snapshot, err := captureWorktreeSnapshot(ctx, workdir)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(snapshot))
	for path := range snapshot {
		files = append(files, path)
	}
	sort.Strings(files)
	return writeJSONWithNewline(filepath.Join(runDir, preexistingWorktreeArtifact), PreexistingWorktree{
		SchemaVersion: ArtifactSchemaVersion,
		Baseline:      baseline,
		Files:         files,
		CapturedAt:    time.Now().UTC(),
	})
}
