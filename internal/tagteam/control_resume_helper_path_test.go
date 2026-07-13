package tagteam

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type helperFX struct {
	repo, runID, runDir, external, sentinel string
	ctx                                     context.Context
	gate                                    *controlResumePathGate
}

func newHelperFX(t *testing.T, id string, replace bool) helperFX {
	t.Helper()
	repo, baseline := createResumeFixtureRepo(t)
	stateRoot := t.TempDir()
	runDir, err := createRunDir(repo, stateRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	writeResumeFixture(t, runDir, id, repo, baseline, RunStatusRunning)
	for _, p := range [][2]string{
		{filepath.Join(runDir, "input.md"), "in-root prompt\n"},
		{filepath.Join(runDir, "supervisor-brief.md"), "in-root brief\n"},
		{filepath.Join(runDir, "broken.json"), `{"not":"valid"`},
		{filepath.Join(repo, "AGENTS.md"), "follow repo rules\n"},
	} {
		mustWriteFile(t, p[0], p[1])
	}
	gate, err := newControlResumePathGate(repo, stateRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	fx := helperFX{repo: repo, runID: id, runDir: runDir, gate: gate, ctx: withControlResumeGate(context.Background(), gate)}
	if !replace {
		return fx
	}
	fx.external = filepath.Join(t.TempDir(), "ext")
	_ = os.MkdirAll(fx.external, 0o700)
	writeResumeFixture(t, fx.external, id, repo, baseline, RunStatusRunning)
	mustWriteFile(t, filepath.Join(fx.external, "input.md"), "EXTERNAL_PROMPT_LEAK\n")
	mustWriteFile(t, filepath.Join(fx.external, "supervisor-brief.md"), "EXTERNAL_BRIEF_LEAK\n")
	fx.sentinel = filepath.Join(fx.external, "sentinel.txt")
	mustWriteFile(t, fx.sentinel, "external_helper_untouched")
	replaceRunDirSymlink(t, runDir, fx.external)
	return fx
}
func replaceRunDirSymlink(t *testing.T, runDir, external string) {
	t.Helper()
	bak := runDir + ".helper.bak"
	if err := os.Rename(runDir, bak); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, runDir); err != nil {
		_ = os.Rename(bak, runDir)
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(runDir)
		_ = os.Rename(bak, runDir)
	})
}
func assertPreflight(t *testing.T, err error) {
	t.Helper()
	var e *ExitError
	if err == nil || !errors.As(err, &e) || e.Code != ExitPreflightFailed {
		t.Fatalf("got %v (%T), want ExitPreflightFailed", err, err)
	}
}
func samplePlan(id string) *ExecutionPlan {
	now := time.Now().UTC()
	return &ExecutionPlan{SchemaVersion: ArtifactSchemaVersion, RunID: id, Mode: ModeSupervisor, Status: "running",
		Items:  []PlanItem{{ID: "P1", Title: "one", Status: PlanStatusInProgress, Source: "t"}},
		Events: []PlanEvent{{Type: "status", By: "t", At: now, To: PlanStatusInProgress, Message: "s"}}, CreatedAt: now, UpdatedAt: now}
}
func TestControlResumeHelpersRejectRunDirReplacement(t *testing.T) {
	fx := newHelperFX(t, "helper-reject", true)
	untouched := func(err error, names ...string) {
		t.Helper()
		assertPreflight(t, err)
		if d, e := os.ReadFile(fx.sentinel); e != nil || string(d) != "external_helper_untouched" {
			t.Fatalf("sentinel %s %v", d, e)
		}
		for _, n := range names {
			if _, e := os.Stat(filepath.Join(fx.external, n)); !os.IsNotExist(e) {
				t.Fatalf("wrote %s: %v", n, e)
			}
		}
	}
	_, err := loadAndPersistRepoInstructions(fx.ctx, RunOptions{Workdir: fx.repo, RespectRepoInstructions: true}, fx.runDir)
	untouched(err, "repo-instructions.md", "repo-instructions.json")
	untouched(persistExecutionPlan(fx.ctx, fx.runDir, samplePlan(fx.runID)), "plan.json", "plan-events.jsonl")
	_, err = runBaselineTest(fx.ctx, RunOptions{Workdir: fx.repo, TestCmd: "printf X", Timeout: 5 * time.Second}, fx.runDir)
	untouched(err, "baseline-test.txt", hostActivityArtifact)
	raw, err := readRepairSource(fx.ctx, fx.runDir, filepath.Join(fx.runDir, "broken.json"), nil)
	untouched(err)
	if raw != nil {
		t.Fatal(raw)
	}
	var seen []string
	_, _, att, err := NewApp(DefaultConfig()).repairJSONWithWorker(fx.ctx, RunOptions{
		JSONRepair: "worker", Coder: RoleTarget{Adapter: "fake"}, Workdir: fx.repo, Timeout: 5 * time.Second,
	}, map[string]Adapter{"fake": recordingResumeAdapter{seen: &seen}}, fx.runDir, filepath.Join(fx.runDir, "broken.json"),
		"c", `{}`, []byte(`{`), errors.New("bad"))
	if !att || len(seen) != 0 {
		t.Fatalf("att=%v seen=%v", att, seen)
	}
	untouched(err, "broken.json.repair-prompt.md", "broken.json.repaired.json")
	p, err := readControlRunPrompt(fx.ctx, fx.runDir, "fb")
	untouched(err)
	if strings.Contains(p, "EXTERNAL_PROMPT_LEAK") {
		t.Fatal(p)
	}
	relay, err := loadResumeRelayContextControl(fx.ctx, fx.runDir)
	untouched(err)
	if strings.Contains(relay.Brief, "EXTERNAL_BRIEF_LEAK") {
		t.Fatal(relay.Brief)
	}
	mustWriteFile(t, filepath.Join(fx.external, "plan.json"), `{"summary":"EXTERNAL_PLAN_LEAK"}`)
	plan, err := readControlExecutionPlanOptional(fx.ctx, fx.runDir)
	untouched(err)
	if plan != nil {
		t.Fatal(plan)
	}
	base := filepath.Join(fx.runDir, "c.json")
	untouched(noteJSONRepairFailure(fx.ctx, fx.runDir, base, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("path changed")}, nil))
	untouched(writeRepairSideBytes(fx.ctx, fx.runDir, base, ".x.txt", []byte("x"), nil))
}
func TestControlResumeRepairExternalBaseBrokenSymlinkAndRetry(t *testing.T) {
	fx := newHelperFX(t, "helper-repair-retry", false)
	outside := filepath.Join(t.TempDir(), "never-create", "nested")
	var seen []string
	doRepair := func(base string) error {
		_, _, att, err := NewApp(DefaultConfig()).repairJSONWithWorker(fx.ctx, RunOptions{
			JSONRepair: "worker", Coder: RoleTarget{Adapter: "fake"}, Workdir: fx.repo, Timeout: 5 * time.Second,
		}, map[string]Adapter{"fake": recordingResumeAdapter{seen: &seen}}, fx.runDir, base, "c", `{}`, []byte(`{`), errors.New("bad"))
		if !att || len(seen) != 0 {
			t.Fatalf("att=%v seen=%v", att, seen)
		}
		return err
	}
	assertPreflight(t, doRepair(filepath.Join(outside, "c.json")))
	if _, e := os.Stat(outside); !os.IsNotExist(e) {
		t.Fatalf("mkdir leaked: %v", e)
	}
	nested := filepath.Join(fx.runDir, "nested-escape")
	if err := os.Symlink(t.TempDir(), nested); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	assertPreflight(t, doRepair(filepath.Join(nested, "c.json")))
	base := filepath.Join(fx.runDir, "side.json")
	if err := os.Symlink(filepath.Join(fx.runDir, "missing"), base+".repair-failed.txt"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := writeRepairSideBytes(fx.ctx, fx.runDir, base, ".repair-failed.txt", []byte("x"), nil)
	assertPreflight(t, err)
	if !isControlResumePathGateError(err) {
		t.Fatal(err)
	}
	assertPreflight(t, noteJSONRepairFailure(fx.ctx, fx.runDir, base, &ExitError{Code: ExitPreflightFailed, Err: fmt.Errorf("control path is a broken symlink")}, nil))

	// Coder + reviewer: write failure after rebind aborts without second dispatch.
	for _, kind := range []string{"coder", "reviewer"} {
		calls := 0
		if kind == "coder" {
			out := filepath.Join(fx.runDir, "worker-round-1.md")
			ad := fakeDirectAdapter{
				build: func(role Role, req Request) (*CommandSpec, error) {
					return &CommandSpec{Argv: []string{"fake"}, Dir: fx.repo, Output: req.OutputPath}, nil
				},
				direct: func(role Role, req Request) (Result, error) {
					calls++
					if calls > 1 {
						t.Fatal("coder retry")
					}
					return Result{Raw: []byte("I am a model identity reply.")}, nil
				},
			}
			controlResumeBeforeContractRetryHook = func() { _ = os.MkdirAll(out+".retry-prompt.md", 0o755) }
			before, e := captureWorktreeSnapshot(context.Background(), fx.repo)
			if e != nil {
				t.Fatal(e)
			}
			_, err = NewApp(DefaultConfig()).runEditorWithContractRetry(fx.ctx, RunOptions{Workdir: fx.repo, Mode: ModeSupervisor, Timeout: 10 * time.Second}, ad, Request{
				Context: fx.ctx, Prompt: "x", Workdir: fx.repo, RunDir: fx.runDir, OutputPath: out, Timeout: 10 * time.Second,
				Phase: "worker", RequireWorkerContract: true, controlResumeGate: fx.gate,
			}, before)
			controlResumeBeforeContractRetryHook = nil
			if err == nil || calls != 1 {
				t.Fatalf("coder write-fail err=%v calls=%d", err, calls)
			}
			continue
		}
		schema, diff := filepath.Join(fx.runDir, "review-schema.json"), filepath.Join(fx.runDir, "diff.patch")
		mustWriteFile(t, schema, ReviewSchema)
		mustWriteFile(t, diff, "diff\n")
		ad := fakeDirectAdapter{
			build: func(role Role, req Request) (*CommandSpec, error) {
				return &CommandSpec{Argv: []string{"fake"}, Dir: fx.repo, Output: req.OutputPath}, nil
			},
			direct: func(role Role, req Request) (Result, error) {
				calls++
				if calls > 1 {
					t.Fatal("reviewer retry")
				}
				return Result{Raw: []byte(`{}`)}, &OutputContractError{Err: fmt.Errorf("schema")}
			},
		}
		testRegistryOverrides = map[string]Adapter{"fake": ad}
		controlResumeBeforeContractRetryHook = func() {
			_ = os.MkdirAll(filepath.Join(fx.runDir, "supervisor-round-1.json.retry-prompt.md"), 0o755)
		}
		final := FinalRun{Adapters: map[string]string{"supervisor": "fake"}, Models: map[string]string{"supervisor": "test"}}
		initFinalState(&final, RunOptions{})
		_, _, _, err = NewApp(DefaultConfig()).runAdversary(fx.ctx, RunOptions{
			Prompt: "r", Workdir: fx.repo, Mode: ModeSupervisor, Adversary: RoleTarget{Adapter: "fake", Model: "t"},
			Timeout: 10 * time.Second, MaxOutputBytes: 2 << 20,
		}, 1, fx.runDir, schema, "r", "HEAD", "diff", diff, "", "", nil, RelayContext{}, "", &final)
		testRegistryOverrides, controlResumeBeforeContractRetryHook = nil, nil
		if err == nil || calls != 1 {
			t.Fatalf("reviewer write-fail err=%v calls=%d", err, calls)
		}
	}

	// Replacement between contract failure and retry.
	fx2 := newHelperFX(t, "helper-retry-replace", false)
	external := filepath.Join(t.TempDir(), "ext")
	_ = os.MkdirAll(external, 0o700)
	writeResumeFixture(t, external, fx2.runID, fx2.repo, "HEAD", RunStatusRunning)
	mustWriteFile(t, filepath.Join(external, "sentinel.txt"), "coder_retry_untouched")
	calls := 0
	ad := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: fx2.repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			calls++
			if calls > 1 {
				t.Fatal("retry after replace")
			}
			return Result{Raw: []byte("I am a model identity reply.")}, nil
		},
	}
	controlResumeBeforeContractRetryHook = func() { replaceRunDirSymlink(t, fx2.runDir, external) }
	t.Cleanup(func() { controlResumeBeforeContractRetryHook = nil })
	before, err := captureWorktreeSnapshot(context.Background(), fx2.repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewApp(DefaultConfig()).runEditorWithContractRetry(fx2.ctx, RunOptions{Workdir: fx2.repo, Mode: ModeSupervisor, Timeout: 10 * time.Second}, ad, Request{
		Context: fx2.ctx, Prompt: "x", Workdir: fx2.repo, RunDir: fx2.runDir, OutputPath: filepath.Join(fx2.runDir, "worker-round-1.md"),
		Timeout: 10 * time.Second, Phase: "worker", RequireWorkerContract: true, controlResumeGate: fx2.gate,
	}, before)
	assertPreflight(t, err)
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if d, _ := os.ReadFile(filepath.Join(external, "sentinel.txt")); string(d) != "coder_retry_untouched" {
		t.Fatalf("sentinel %s", d)
	}
}
func TestControlResumeRepairRejectsReplacementAfterMkdir(t *testing.T) {
	fx := newHelperFX(t, "helper-repair-after-mkdir", false)
	external := filepath.Join(t.TempDir(), "ext")
	_ = os.MkdirAll(external, 0o700)
	writeResumeFixture(t, external, fx.runID, fx.repo, "HEAD", RunStatusRunning)
	mustWriteFile(t, filepath.Join(external, "sentinel.txt"), "repair_after_mkdir_untouched")
	var seen []string
	controlResumeAfterRepairMkdirHook = func() { replaceRunDirSymlink(t, fx.runDir, external) }
	t.Cleanup(func() { controlResumeAfterRepairMkdirHook = nil })

	_, _, att, err := NewApp(DefaultConfig()).repairJSONWithWorker(fx.ctx, RunOptions{
		JSONRepair: "worker", Coder: RoleTarget{Adapter: "fake"}, Workdir: fx.repo, Timeout: 5 * time.Second,
	}, map[string]Adapter{"fake": recordingResumeAdapter{seen: &seen}}, fx.runDir, filepath.Join(fx.runDir, "broken.json"),
		"c", `{}`, []byte(`{`), errors.New("bad"))
	assertPreflight(t, err)
	if !att || len(seen) != 0 {
		t.Fatalf("att=%v seen=%v", att, seen)
	}
	if d, e := os.ReadFile(filepath.Join(external, "sentinel.txt")); e != nil || string(d) != "repair_after_mkdir_untouched" {
		t.Fatalf("sentinel %s %v", d, e)
	}
	for _, name := range []string{"broken.json.repair-prompt.md", "broken.json.repaired.json", "broken.json.repair-failed.txt"} {
		if _, e := os.Stat(filepath.Join(external, name)); !os.IsNotExist(e) {
			t.Fatalf("wrote external %s: %v", name, e)
		}
	}
}

func TestControlResumeRepairSideWriteFailurePropagates(t *testing.T) {
	fx := newHelperFX(t, "helper-repair-side-fail", false)
	base := filepath.Join(fx.runDir, "side-fail.json")
	// Force a durable write failure that is not a path-gate preflight error.
	if err := os.MkdirAll(base+".repair-failed.txt", 0o755); err != nil {
		t.Fatal(err)
	}

	err := noteJSONRepairFailure(fx.ctx, fx.runDir, base, fmt.Errorf("adapter boom"), nil)
	if err == nil {
		t.Fatal("expected side-artifact write failure to propagate")
	}
	if isControlResumePathGateError(err) {
		t.Fatalf("want non-preflight write error, got preflight: %v", err)
	}
	// Durable write refuses non-regular targets; ensure that failure is surfaced
	// rather than swallowing the note and continuing with a soft nil.
	if !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("want durable non-regular write error, got: %v", err)
	}

	// Adapter-error branch in repairJSONWithWorker must also surface writeErr.
	failing := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: fx.repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			return Result{}, fmt.Errorf("repair adapter failed")
		},
	}
	artifactBase := filepath.Join(fx.runDir, "adapter-side-fail.json")
	if err := os.MkdirAll(artifactBase+".repair-failed.txt", 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, att, err := NewApp(DefaultConfig()).repairJSONWithWorker(fx.ctx, RunOptions{
		JSONRepair: "worker", Coder: RoleTarget{Adapter: "fake"}, Workdir: fx.repo, Timeout: 5 * time.Second,
	}, map[string]Adapter{"fake": failing}, fx.runDir, artifactBase, "c", `{}`, []byte(`{`), errors.New("bad"))
	if !att {
		t.Fatal("expected repair attempt")
	}
	if err == nil {
		t.Fatal("expected repair-failed side write error from adapter branch")
	}
	if isControlResumePathGateError(err) {
		t.Fatalf("want durable write error, got preflight: %v", err)
	}
	if strings.Contains(err.Error(), "repair adapter failed") {
		t.Fatalf("side write error was suppressed; got original adapter error: %v", err)
	}
	if !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("want durable non-regular write error from adapter branch, got: %v", err)
	}
}

func TestControlResumeHelpersUngatedCLIBehaviorUnchanged(t *testing.T) {
	fx := newHelperFX(t, "helper-ungated", false)
	ctx := context.Background()
	text, err := loadAndPersistRepoInstructions(ctx, RunOptions{Workdir: fx.repo, RespectRepoInstructions: true}, fx.runDir)
	if err != nil || !strings.Contains(text, "follow repo rules") {
		t.Fatalf("repo %q %v", text, err)
	}
	if err := persistExecutionPlan(ctx, fx.runDir, samplePlan(fx.runID)); err != nil {
		t.Fatal(err)
	}
	bt, err := runBaselineTest(ctx, RunOptions{Workdir: fx.repo, TestCmd: "true", Timeout: 5 * time.Second}, fx.runDir)
	if err != nil || bt == nil || !bt.Passed {
		t.Fatalf("baseline %#v %v", bt, err)
	}
	nested := filepath.Join(fx.runDir, "nested", "out", "advisory.json")
	if rebuildControlResumeArtifactPath(fx.runDir, fx.runDir, nested) != nested || controlResumeGateFrom(ctx) != nil {
		t.Fatal("ungated nested path or gate regression")
	}
	p, err := readControlRunPrompt(ctx, fx.runDir, "")
	if err != nil || strings.TrimSpace(p) != "in-root prompt" {
		t.Fatalf("prompt %q %v", p, err)
	}
	relay, err := loadResumeRelayContextControl(ctx, fx.runDir)
	if err != nil || strings.TrimSpace(relay.Brief) != "in-root brief" {
		t.Fatalf("relay %#v %v", relay, err)
	}
	plan, err := readControlExecutionPlanOptional(ctx, fx.runDir)
	if err != nil || plan == nil || plan.RunID != fx.runID {
		t.Fatalf("plan %#v %v", plan, err)
	}
}
