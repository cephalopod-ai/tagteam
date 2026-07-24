package tagteam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type IntegrityViolationError struct {
	Paths []string
}

func (e *IntegrityViolationError) Error() string {
	return fmt.Sprintf("host or read-only integrity violation: %s", strings.Join(e.Paths, ", "))
}

func IsIntegrityViolation(err error) bool {
	var target *IntegrityViolationError
	return errors.As(err, &target)
}

func shouldBlockScoutFailure(policy LossPolicy, err error) bool {
	return IsIntegrityViolation(err) || policyBlocks(policy)
}

type protectedFile struct {
	Path string
	Data []byte
	Mode os.FileMode
}

type integritySnapshot struct {
	Files map[string]protectedFile
}

func validateRequestArtifactPaths(req Request) error {
	if req.RunDir == "" {
		return nil
	}
	runDir, err := canonicalPath(req.RunDir, false)
	if err != nil {
		return err
	}
	for label, path := range map[string]string{"output": req.OutputPath, "schema": req.SchemaPath} {
		if strings.TrimSpace(path) == "" {
			continue
		}
		candidate, pathErr := canonicalPath(path, false)
		if pathErr != nil {
			return pathErr
		}
		if !pathWithin(runDir, candidate) {
			return fmt.Errorf("%s artifact path escapes run directory: %s", label, path)
		}
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func captureIntegritySnapshot(req Request) (integritySnapshot, error) {
	snapshot := integritySnapshot{Files: map[string]protectedFile{}}
	if req.Workdir != "" {
		legacyRunDir := ""
		if req.RunDir != "" {
			legacyRoot := filepath.Join(req.Workdir, ".tagteam")
			if pathWithin(legacyRoot, req.RunDir) {
				legacyRunDir = filepath.Clean(req.RunDir)
			}
		}
		if err := snapshot.captureTree(filepath.Join(req.Workdir, ".tagteam"), func(path string) bool {
			return legacyRunDir == "" || !pathWithin(legacyRunDir, filepath.Clean(path))
		}); err != nil {
			return integritySnapshot{}, err
		}
	}
	if req.RunDir != "" {
		if err := snapshot.captureTree(req.RunDir, func(path string) bool {
			// Invocation-scoped temp files may legitimately contain names such as
			// .dory/state.json. They are not host control artifacts.
			if pathWithin(filepath.Join(req.RunDir, "tmp"), filepath.Clean(path)) {
				return false
			}
			if req.OutputPath != "" && (path == req.OutputPath || strings.HasPrefix(path, req.OutputPath+".")) {
				return false
			}
			return protectedRunArtifact(filepath.Base(path))
		}); err != nil {
			return integritySnapshot{}, err
		}
	}
	return snapshot, nil
}

func protectedRunArtifact(name string) bool {
	if name == "meta.json" || name == "state.json" || name == "events.jsonl" || name == findingsLedgerFilename || name == "review-schema.json" || name == "worker-schema.json" || name == "plan.json" {
		return true
	}
	return strings.HasPrefix(name, "diff-round-") || strings.HasPrefix(name, "quality-gates-") || strings.HasPrefix(name, "review-bundle-")
}

func (s integritySnapshot) captureTree(root string, include func(string) bool) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("protected path is not a directory: %s", root)
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !include(path) {
			return nil
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("protected artifact is not a regular file: %s", path)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		s.Files[path] = protectedFile{Path: path, Data: data, Mode: fileInfo.Mode().Perm()}
		return nil
	})
}

func verifyAndRestoreIntegrity(req Request, before integritySnapshot) ([]string, error) {
	after, err := captureIntegritySnapshot(req)
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for path, original := range before.Files {
		current, ok := after.Files[path]
		if ok && string(current.Data) == string(original.Data) && current.Mode == original.Mode {
			continue
		}
		changed = append(changed, path)
		if err := writeFileDurable(path, original.Data, original.Mode, true); err != nil {
			return changed, err
		}
	}
	for path := range after.Files {
		if _, ok := before.Files[path]; ok {
			continue
		}
		changed = append(changed, path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return changed, err
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func validateInvocationIntegrity(ctx context.Context, req Request, role Role, gitBefore worktreeSnapshot, hostBefore integritySnapshot) error {
	violations := []string{}
	if role != RoleCoder && req.Workdir != "" {
		after, err := captureWorktreeSnapshot(ctx, req.Workdir)
		if err != nil {
			return err
		}
		for _, path := range worktreeContentDelta(gitBefore, after) {
			violations = append(violations, "git:"+path)
		}
	}
	hostChanges, err := verifyAndRestoreIntegrity(req, hostBefore)
	if err != nil {
		return err
	}
	for _, path := range hostChanges {
		violations = append(violations, "host:"+path)
	}
	if len(violations) == 0 {
		return nil
	}
	artifact := map[string]any{
		"schema_version": ArtifactSchemaVersion,
		"role":           role,
		"paths":          violations,
		"restored_host":  hostChanges,
		"recorded_at":    time.Now().UTC(),
	}
	if req.RunDir != "" {
		_ = writeJSONWithNewline(filepath.Join(req.RunDir, "integrity-violation.json"), artifact)
	}
	return &IntegrityViolationError{Paths: violations}
}
