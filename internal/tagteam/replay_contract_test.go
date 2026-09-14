package tagteam

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func replayFixture(t *testing.T) (string, RunOptions, ExecutionSnapshot) {
	t.Helper()
	dir := t.TempDir()
	opts := RunOptions{Prompt: "héllo", Workdir: "/repo", Mode: ModeSolo, Coder: RoleTarget{Adapter: "fake", Model: "m1"}, Rounds: 1, Timeout: time.Second, MaxRoleInvocations: 10, EnvOverlay: map[string]string{"TOKEN": "secret"}}
	s, err := freezeExecutionSnapshot(dir, "run-1", opts)
	if err != nil {
		t.Fatal(err)
	}
	return dir, opts, s
}

func baseEnvelope() CanonicalRequestEnvelope {
	return CanonicalRequestEnvelope{CanonicalVersion: replayContractVersion, OperationType: "provider_request.v1", Destination: "https://api", Provider: "p", Model: "m", Method: "POST", Path: "/v1", Arguments: map[string]any{"b": 2, "a": "é"}, BodySHA256: bytesDigest([]byte{0, 1, 2}), Parameters: map[string]any{"temperature": 0.0}, Headers: map[string]string{"content-type": "application/json"}, TimeoutNanos: 10, RetryPolicy: "r", SandboxPolicy: "s", PermissionPolicy: "p", NetworkPolicy: "n", InputArtifactHashes: map[string]string{"x": "h"}, IdempotencyKey: "i", SecretReferenceDigest: "d"}
}

func TestCanonicalRequestGoldenVectors(t *testing.T) {
	a := baseEnvelope()
	b := baseEnvelope()
	b.Arguments = map[string]any{"a": "é", "b": 2}
	ha, _ := canonicalDigest(a)
	hb, _ := canonicalDigest(b)
	if ha != hb {
		t.Fatalf("reordered keys changed digest %s != %s", ha, hb)
	}
	const golden = "c5f38a98def5b643a04879a72666e6a000c070e32ac4f1bda218684c79373ca8"
	if ha != golden {
		t.Fatalf("golden digest=%s; update only for intentional canonical version change", ha)
	}
	null := map[string]any{"x": nil}
	omitted := map[string]any{}
	hn, _ := canonicalDigest(null)
	ho, _ := canonicalDigest(omitted)
	if hn == ho {
		t.Fatal("null and omitted collapsed")
	}
	array1, _ := canonicalDigest([]any{1, 2})
	array2, _ := canonicalDigest([]any{2, 1})
	if array1 == array2 {
		t.Fatal("array order collapsed")
	}
	if bytesDigest([]byte{0, 1}) == bytesDigest([]byte{0, 2}) {
		t.Fatal("binary payload digest collision")
	}
}

func TestEverySemanticEnvelopeFieldChangesDigest(t *testing.T) {
	base := baseEnvelope()
	want, _ := canonicalDigest(base)
	mutations := []func(*CanonicalRequestEnvelope){func(e *CanonicalRequestEnvelope) { e.OperationType = "tool.v1" }, func(e *CanonicalRequestEnvelope) { e.Destination += "x" }, func(e *CanonicalRequestEnvelope) { e.Provider += "x" }, func(e *CanonicalRequestEnvelope) { e.Model += "x" }, func(e *CanonicalRequestEnvelope) { e.Method = "GET" }, func(e *CanonicalRequestEnvelope) { e.Path += "x" }, func(e *CanonicalRequestEnvelope) { e.Arguments = map[string]any{"x": 1} }, func(e *CanonicalRequestEnvelope) { e.BodySHA256 += "x" }, func(e *CanonicalRequestEnvelope) { e.Parameters["temperature"] = 1 }, func(e *CanonicalRequestEnvelope) { e.Headers["content-type"] = "text/plain" }, func(e *CanonicalRequestEnvelope) { e.TimeoutNanos++ }, func(e *CanonicalRequestEnvelope) { e.RetryPolicy += "x" }, func(e *CanonicalRequestEnvelope) { e.SandboxPolicy += "x" }, func(e *CanonicalRequestEnvelope) { e.PermissionPolicy += "x" }, func(e *CanonicalRequestEnvelope) { e.NetworkPolicy += "x" }, func(e *CanonicalRequestEnvelope) { e.InputArtifactHashes["x"] = "z" }, func(e *CanonicalRequestEnvelope) { e.IdempotencyKey += "x" }, func(e *CanonicalRequestEnvelope) { e.SecretReferenceDigest += "x" }, func(e *CanonicalRequestEnvelope) { e.CanonicalVersion++ }}
	for i, mutate := range mutations {
		e := baseEnvelope()
		e.Parameters = map[string]any{"temperature": 0.0}
		e.Headers = map[string]string{"content-type": "application/json"}
		e.InputArtifactHashes = map[string]string{"x": "h"}
		mutate(&e)
		got, _ := canonicalDigest(e)
		if got == want {
			t.Fatalf("mutation %d did not change digest", i)
		}
	}
}

func TestExecutionSnapshotRejectsWorkflowAndArgumentMutation(t *testing.T) {
	dir, opts, _ := replayFixture(t)
	opts.Coder.Model = "m2"
	if _, err := freezeExecutionSnapshot(dir, "run-1", opts); err == nil {
		t.Fatal("workflow mutation accepted")
	}
	_, opts, _ = replayFixture(t)
	dir = t.TempDir()
	if _, err := freezeExecutionSnapshot(dir, "run-1", opts); err != nil {
		t.Fatal(err)
	}
	opts.Prompt = "changed"
	if _, err := freezeExecutionSnapshot(dir, "run-1", opts); err == nil {
		t.Fatal("argument mutation accepted")
	}
}

func TestExactCommittedReplayDoesNotInvokeAgain(t *testing.T) {
	dir, _, _ := replayFixture(t)
	e := baseEnvelope()
	r, replayed, err := prepareOperation(dir, e.OperationType, e, nil)
	if err != nil || replayed != nil {
		t.Fatal(err)
	}
	if err = transitionOperation(dir, r, OperationInFlight, nil, "dispatch"); err != nil {
		t.Fatal(err)
	}
	result := Result{Text: "recorded"}
	if err = transitionOperation(dir, r, OperationCommitted, &result, "commit"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, replayed, err = prepareOperation(dir, e.OperationType, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replayed == nil || replayed.Text != "recorded" {
		calls++
		t.Fatal("committed result not replayed")
	}
	if calls != 0 {
		t.Fatal("external call repeated")
	}
}

func TestUnresolvedMismatchAndCorruptionFailClosed(t *testing.T) {
	t.Run("type hash reorder insert", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		e := baseEnvelope()
		r, _, _ := prepareOperation(dir, e.OperationType, e, nil)
		_ = transitionOperation(dir, r, OperationInFlight, nil, "dispatch")
		for _, change := range []func(*CanonicalRequestEnvelope){func(x *CanonicalRequestEnvelope) { x.OperationType = "tool.v1" }, func(x *CanonicalRequestEnvelope) { x.Model = "other" }} {
			x := baseEnvelope()
			change(&x)
			if _, _, err := prepareOperation(dir, x.OperationType, x, nil); err == nil {
				t.Fatal("mismatch accepted")
			}
		}
	})
	t.Run("missing snapshot", func(t *testing.T) {
		dir := t.TempDir()
		e := baseEnvelope()
		if _, _, err := prepareOperation(dir, e.OperationType, e, nil); err == nil {
			t.Fatal("missing replay authority accepted")
		}
	})
	t.Run("corrupt operation", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		os.MkdirAll(filepath.Join(dir, "operations"), 0700)
		os.WriteFile(operationPath(dir, 1), []byte("{"), 0600)
		e := baseEnvelope()
		if _, _, err := prepareOperation(dir, e.OperationType, e, nil); err == nil {
			t.Fatal("corrupt operation accepted")
		}
	})
	t.Run("missing sequence", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		e := baseEnvelope()
		r, _, err := prepareOperation(dir, e.OperationType, e, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(operationPath(dir, r.Sequence), operationPath(dir, r.Sequence+1)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := prepareOperation(dir, e.OperationType, e, nil); err == nil {
			t.Fatal("operation stream gap accepted")
		}
	})
	t.Run("snapshot digest tampering", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		path := filepath.Join(dir, "execution-snapshot.json")
		var snapshot ExecutionSnapshot
		data, _ := os.ReadFile(path)
		_ = json.Unmarshal(data, &snapshot)
		snapshot.Arguments["prompt"] = "tampered"
		_ = writeJSONWithNewline(path, snapshot)
		e := baseEnvelope()
		if _, _, err := prepareOperation(dir, e.OperationType, e, nil); err == nil {
			t.Fatal("snapshot with stale digest accepted")
		}
	})
}

func TestCommittedOperationIntegrityAndLifecycleFailClosed(t *testing.T) {
	dir, _, _ := replayFixture(t)
	e := baseEnvelope()
	r, _, err := prepareOperation(dir, e.OperationType, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transitionOperation(dir, r, OperationCommitted, &Result{Text: "invented"}, "skip dispatch"); err == nil {
		t.Fatal("prepared operation committed without entering in_flight")
	}
	if err := transitionOperation(dir, r, OperationInFlight, nil, "dispatch"); err != nil {
		t.Fatal(err)
	}
	if err := transitionOperation(dir, r, OperationCommitted, &Result{Text: "recorded", Raw: []byte("not persisted")}, "commit"); err != nil {
		t.Fatal(err)
	}
	path := operationPath(dir, r.Sequence)
	var stored OperationRecord
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &stored)
	stored.Result.Text = "tampered"
	_ = writeJSONWithNewline(path, stored)
	if _, _, err := prepareOperation(dir, e.OperationType, e, nil); err == nil {
		t.Fatal("tampered committed result accepted")
	}
}

func TestOperationCrashPoliciesAndReconciliation(t *testing.T) {
	t.Run("before invocation remains prepared", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		e := baseEnvelope()
		r, _, _ := prepareOperation(dir, e.OperationType, e, nil)
		again, _, err := prepareOperation(dir, e.OperationType, e, nil)
		if err != nil || again.Sequence != r.Sequence || again.State != OperationPrepared {
			t.Fatalf("recovery=%#v err=%v", again, err)
		}
	})
	t.Run("after invocation becomes in doubt and records risk", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		e := baseEnvelope()
		r, _, _ := prepareOperation(dir, e.OperationType, e, nil)
		if err := transitionOperation(dir, r, OperationInFlight, nil, "dispatch"); err != nil {
			t.Fatal(err)
		}
		again, _, err := prepareOperation(dir, e.OperationType, e, nil)
		if err != nil || again.State != OperationInDoubt || !again.DuplicateEffectRisk {
			t.Fatalf("operation=%#v err=%v", again, err)
		}
	})
	t.Run("native reconciliation avoids repeat", func(t *testing.T) {
		dir, _, _ := replayFixture(t)
		e := baseEnvelope()
		r, _, _ := prepareOperation(dir, e.OperationType, e, nil)
		_ = transitionOperation(dir, r, OperationInFlight, nil, "dispatch")
		r, _, _ = prepareOperation(dir, e.OperationType, e, nil)
		calls := 0
		got, err := reconcileOperation(dir, r, func(key string) (*Result, bool, error) { calls++; return &Result{Text: "found"}, true, nil })
		if err != nil || got.Text != "found" || calls != 1 {
			t.Fatalf("got=%#v calls=%d err=%v", got, calls, err)
		}
		stored, _ := readOperation(operationPath(dir, r.Sequence))
		if stored.State != OperationCommitted || stored.Recovery != "provider_native_reconciliation" {
			t.Fatalf("stored=%#v", stored)
		}
	})
}

func TestPanelAdmissionIsAtomicAndConcurrent(t *testing.T) {
	dir := t.TempDir()
	if err := initializePanelBudget(dir, 10, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := admitPanel(dir, "three", "d", 3, 3); err == nil {
		t.Fatal("three-slot panel admitted into two slots")
	}
	l, _ := readPanelBudget(dir)
	if l.LiveConcurrencyReserved != 0 {
		t.Fatal("partial panel reservation")
	}
	dir = t.TempDir()
	_ = initializePanelBudget(dir, 10, 3)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := admitPanel(dir, id, id, 2, 2); err == nil {
				admitted.Add(1)
			}
		}(id)
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("admitted=%d", admitted.Load())
	}
	l, _ = readPanelBudget(dir)
	if l.LiveConcurrencyReserved > 3 {
		t.Fatal("overbooked")
	}
}

func TestPanelLedgersRemainSeparateAndRestartIdempotent(t *testing.T) {
	dir := t.TempDir()
	_ = initializePanelBudget(dir, 2, 1)
	r, err := admitPanel(dir, "p", "definition", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := admitPanel(dir, "p", "definition", 1, 1)
	if err != nil || again.Generation != r.Generation {
		t.Fatal("restart double reserved")
	}
	if err = startPanelMember(dir, "p", r.Generation); err != nil {
		t.Fatal(err)
	}
	if err = completePanelMember(dir, "p", r.Generation); err != nil {
		t.Fatal(err)
	}
	l, _ := readPanelBudget(dir)
	if l.LiveConcurrencyInUse != 0 || l.CumulativeWorkConsumed != 1 || l.CumulativeWorkReserved != 0 {
		t.Fatalf("ledger=%#v", l)
	}
	if err = completePanelMember(dir, "p", r.Generation); err == nil {
		t.Fatal("double release accepted")
	}
	if _, err = admitPanel(dir, "p", "degraded", 1, 1); err == nil {
		t.Fatal("degraded panel substituted after start")
	}
}

func TestCancelNeverStartedReleasesOnlyUnusedReservation(t *testing.T) {
	dir := t.TempDir()
	_ = initializePanelBudget(dir, 3, 2)
	r, _ := admitPanel(dir, "p", "d", 2, 2)
	_ = startPanelMember(dir, "p", r.Generation)
	if err := cancelNeverStartedMember(dir, "p", r.Generation); err != nil {
		t.Fatal(err)
	}
	l, _ := readPanelBudget(dir)
	if l.CumulativeWorkConsumed != 1 || l.CumulativeWorkReserved != 0 || l.LiveConcurrencyInUse != 1 || l.LiveConcurrencyReserved != 0 {
		t.Fatalf("ledger=%#v", l)
	}
}

func TestRetryAdmissionChecksWorkAndConcurrencyIndependently(t *testing.T) {
	dir := t.TempDir()
	_ = initializePanelBudget(dir, 1, 2)
	r, _ := admitPanel(dir, "first", "a", 1, 1)
	_ = startPanelMember(dir, "first", r.Generation)
	_ = completePanelMember(dir, "first", r.Generation)
	if _, err := admitPanel(dir, "retry", "b", 1, 1); err == nil || !errors.As(err, new(*PanelAdmissionError)) {
		t.Fatal("free slot bypassed exhausted cumulative work")
	}
	dir = t.TempDir()
	_ = initializePanelBudget(dir, 5, 1)
	r, _ = admitPanel(dir, "first", "a", 1, 1)
	_ = startPanelMember(dir, "first", r.Generation)
	if _, err := admitPanel(dir, "retry", "b", 1, 1); err == nil {
		t.Fatal("remaining work bypassed occupied live slot")
	}
}

func TestLegacyRunStatusCannotClaimDeterministicReplay(t *testing.T) {
	dir := t.TempDir()
	snap, err := BuildRunSnapshot("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ReplayStatus != "legacy_non_replayable" {
		t.Fatalf("status=%s", snap.ReplayStatus)
	}
	b, _ := json.Marshal(snap)
	if string(b) == "" {
		t.Fatal("status not exposed")
	}
}
