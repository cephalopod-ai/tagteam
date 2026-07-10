package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const liveProgressArtifact = "live-progress.json"

// LiveProgress is host-owned runtime evidence collected while an adapter is
// active. Agents never write this artifact.
type LiveProgress struct {
	SchemaVersion int       `json:"schema_version"`
	Phase         string    `json:"phase"`
	Role          Role      `json:"role"`
	Status        string    `json:"status"`
	Elapsed       string    `json:"elapsed"`
	FilesChanged  int       `json:"files_changed"`
	Additions     int       `json:"additions"`
	Deletions     int       `json:"deletions"`
	ChangedFiles  []string  `json:"changed_files,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
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
		Phase:         phase,
		Role:          role,
		Status:        status,
		Elapsed:       shortDuration(time.Since(started)),
		UpdatedAt:     time.Now().UTC(),
	}
	if req.Workdir != "" {
		files, err := liveChangedFiles(ctx, req.Workdir)
		if err != nil {
			return progress, err
		}
		progress.ChangedFiles = files
		progress.FilesChanged = len(files)
		additions, deletions, err := liveNumstat(ctx, req.Workdir)
		if err != nil {
			return progress, err
		}
		progress.Additions = additions
		progress.Deletions = deletions
	}
	if req.RunDir == "" {
		return progress, nil
	}
	if err := writeJSONAtomic(filepath.Join(req.RunDir, liveProgressArtifact), progress); err != nil {
		return progress, err
	}
	return progress, nil
}

func liveChangedFiles(ctx context.Context, workdir string) ([]string, error) {
	out, err := runCommand(ctx, workdir, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if _, renamed, ok := strings.Cut(path, " -> "); ok {
			path = renamed
		}
		if path == ".tagteam" || strings.HasPrefix(path, ".tagteam/") {
			continue
		}
		seen[path] = struct{}{}
	}
	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func liveNumstat(ctx context.Context, workdir string) (int, int, error) {
	out, err := runCommand(ctx, workdir, "git", "diff", "--numstat", "HEAD", "--", ".", ":(exclude).tagteam")
	if err != nil {
		return 0, 0, err
	}
	additions := 0
	deletions := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if value, err := strconv.Atoi(parts[0]); err == nil {
			additions += value
		}
		if value, err := strconv.Atoi(parts[1]); err == nil {
			deletions += value
		}
	}
	return additions, deletions, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".live-progress-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
