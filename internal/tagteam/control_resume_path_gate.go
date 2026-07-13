package tagteam

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// controlResumePathGate retains the canonical runs-root boundary for MCP
// ResumeControl and re-resolves the run directory immediately before each
// resumed-run artifact mutation or adapter request.
type controlResumePathGate struct {
	repositoryRoot string
	stateRoot      string
	runID          string
	runsRoot       string
	runDir         string
}

// newControlResumePathGate resolves and validates the run under the host-derived
// runs root, including control-safe artifact checks. The returned gate is the
// trust boundary for the rest of MCP resume execution.
func newControlResumePathGate(repositoryRoot, stateRoot, runID string) (*controlResumePathGate, error) {
	runDir, locator, err := resolveControlRunDirectory(repositoryRoot, stateRoot, runID)
	if err != nil {
		return nil, err
	}
	runsRoot, err := ensureCanonicalRunsRoot(locator)
	if err != nil {
		return nil, err
	}
	if runDir != runsRoot && !pathWithin(runsRoot, runDir) {
		return nil, fmt.Errorf("run %q escapes the resolved state root", runID)
	}
	if err := ensureControlWritableArtifacts(runDir, "state.json", "meta.json", "final.json", "run.lock", "resume.json", "resume-verify.index"); err != nil {
		return nil, err
	}
	for _, name := range []string{
		"state.json", "meta.json", "final.json", "run.lock",
		"events.jsonl", "input.md", "plan.json",
		"supervisor-brief.md", "supervisor-instructions.md",
		"scout-round-1.json", "post-scout-round-1.json", "supervisor-work-plan.json",
	} {
		if _, err := os.Lstat(filepath.Join(runDir, name)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if _, err := readControlArtifactBytes(runDir, name); err != nil {
			return nil, err
		}
	}
	againRunDir, againLocator, err := resolveControlRunDirectory(repositoryRoot, stateRoot, runID)
	if err != nil {
		return nil, err
	}
	againRoot, err := ensureCanonicalRunsRoot(againLocator)
	if err != nil {
		return nil, err
	}
	if againRunDir != runDir || againRoot != runsRoot {
		return nil, fmt.Errorf("run %q path changed under the resolved state root", runID)
	}
	return &controlResumePathGate{
		repositoryRoot: repositoryRoot,
		stateRoot:      stateRoot,
		runID:          runID,
		runsRoot:       runsRoot,
		runDir:         runDir,
	}, nil
}

type controlResumeGateContextKey struct{}

// withControlResumeGate attaches the MCP resume path gate to ctx so shared
// helpers reached during ResumeControl can re-resolve without every signature
// growing an extra parameter. CLI resume leaves the value unset.
func withControlResumeGate(ctx context.Context, gate *controlResumePathGate) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, controlResumeGateContextKey{}, gate)
}

func controlResumeGateFrom(ctx context.Context) *controlResumePathGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(controlResumeGateContextKey{}).(*controlResumePathGate)
	return gate
}

// rebindControlResumeFromContext re-resolves when a gate is present on ctx.
// Without a gate (CLI resume), runDir is returned unchanged.
func rebindControlResumeFromContext(ctx context.Context, runDir string, final *FinalRun, names ...string) (string, error) {
	return rebindControlResumeRunDir(controlResumeGateFrom(ctx), runDir, final, names...)
}

// bindControlResumeRequest attaches the gate from req or ctx and re-resolves
// RunDir / OutputPath / SchemaPath immediately before adapter work.
func bindControlResumeRequest(ctx context.Context, req *Request, names ...string) error {
	if req == nil {
		return nil
	}
	if req.controlResumeGate == nil {
		req.controlResumeGate = controlResumeGateFrom(ctx)
	}
	return rebindRequestControlResume(req, names...)
}

// rebindRequestControlResume re-resolves the gate on req and rewrites artifact
// paths that still live under the previous RunDir string.
func rebindRequestControlResume(req *Request, names ...string) error {
	if req == nil || req.controlResumeGate == nil {
		return nil
	}
	prev := req.RunDir
	current, err := rebindControlResumeRunDir(req.controlResumeGate, req.RunDir, nil, names...)
	if err != nil {
		return err
	}
	req.RunDir = current
	if prev != "" && prev != current {
		if req.OutputPath != "" && pathWithin(prev, req.OutputPath) {
			rel, relErr := filepath.Rel(prev, req.OutputPath)
			if relErr == nil {
				req.OutputPath = filepath.Join(current, rel)
			}
		}
		if req.SchemaPath != "" && pathWithin(prev, req.SchemaPath) {
			rel, relErr := filepath.Rel(prev, req.SchemaPath)
			if relErr == nil {
				req.SchemaPath = filepath.Join(current, rel)
			}
		}
	} else if current != "" {
		// Even when the string is unchanged, ensure constructed paths still
		// name files under the validated directory after a TOCTOU window.
		if req.OutputPath != "" && filepath.Dir(req.OutputPath) == prev {
			req.OutputPath = filepath.Join(current, filepath.Base(req.OutputPath))
		}
		if req.SchemaPath != "" && filepath.Dir(req.SchemaPath) == prev {
			req.SchemaPath = filepath.Join(current, filepath.Base(req.SchemaPath))
		}
	}
	return nil
}

// guardControlResumeWritePath refuses a host write when the gate reports the
// run directory escaped or the target path no longer lies under it.
func guardControlResumeWritePath(gate *controlResumePathGate, path string) error {
	if gate == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	current, err := gate.ready()
	if err != nil {
		return err
	}
	if err := validateControlWritablePath(current, path); err != nil {
		return err
	}
	// Prefer the original runs-root boundary so a replaced run directory
	// cannot redefine the trust root used for path-within checks.
	if err := validateControlPathWithinBoundary(gate.runsRoot, path, "resolved state root"); err != nil {
		return err
	}
	return nil
}

// current re-resolves the run directory and requires it to remain the same
// canonical path under the original runs root. Fail closed on change, escape,
// or disappearance.
func (g *controlResumePathGate) current() (string, error) {
	if g == nil {
		return "", fmt.Errorf("control resume path gate is required")
	}
	runDir, locator, err := resolveControlRunDirectory(g.repositoryRoot, g.stateRoot, g.runID)
	if err != nil {
		return "", err
	}
	runsRoot, err := ensureCanonicalRunsRoot(locator)
	if err != nil {
		return "", err
	}
	if runsRoot != g.runsRoot {
		return "", fmt.Errorf("run %q path changed under the resolved state root", g.runID)
	}
	if runDir != g.runsRoot && !pathWithin(g.runsRoot, runDir) {
		return "", fmt.Errorf("run %q escapes the resolved state root", g.runID)
	}
	if runDir != g.runDir {
		return "", fmt.Errorf("run %q path changed under the resolved state root", g.runID)
	}
	return g.runDir, nil
}

// ready re-resolves the run directory and optionally re-checks named write
// targets under that directory before host mutation.
func (g *controlResumePathGate) ready(names ...string) (string, error) {
	runDir, err := g.current()
	if err != nil {
		return "", err
	}
	if len(names) > 0 {
		if err := ensureControlWritableArtifacts(runDir, names...); err != nil {
			return "", err
		}
	}
	return runDir, nil
}

// rebind updates final.RunDir to the currently validated run directory when a
// gate is present. CLI resume (nil gate) leaves runDir unchanged.
func rebindControlResumeRunDir(gate *controlResumePathGate, runDir string, final *FinalRun, names ...string) (string, error) {
	if gate == nil {
		return runDir, nil
	}
	current, err := gate.ready(names...)
	if err != nil {
		return "", err
	}
	if final != nil {
		final.RunDir = current
	}
	return current, nil
}
