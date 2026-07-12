package tagteam

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeParseResultSurfacesEnvelopeError(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Execution failed before completion","session_id":"sess_1"}`)
	_, err := adapter.ParseResult(RoleCoder, raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "claude reported error_during_execution") {
		t.Fatalf("error = %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Execution failed before completion") {
		t.Fatalf("error should include the envelope result text: %q", err.Error())
	}
	if IsOutputContractError(err) {
		t.Fatalf("envelope errors are adapter failures, not contract errors: %T", err)
	}
}

func TestClaudeParseResultSurfacesErrorSubtypeWithoutFlag(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"type":"result","subtype":"error_max_turns","is_error":false,"result":""}`)
	_, err := adapter.ParseResult(RoleAdversary, raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "claude reported error_max_turns") || !strings.Contains(err.Error(), "no result text") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestClaudeParseResultSuccessSubtypeStillParses(t *testing.T) {
	adapter := &ClaudeAdapter{}
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"sess_1"}`)
	result, err := adapter.ParseResult(RoleReporter, raw)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestJSONObjectCandidatesPrefersFencedBlocks(t *testing.T) {
	raw := []byte("Ignore {\"decoy\": true} here.\n```json\n{\"fenced\": 1}\n```\ntrailing {\"last\": 2}")
	candidates := jsonObjectCandidates(raw)
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d: %q", len(candidates), candidates)
	}
	if string(candidates[0]) != `{"fenced": 1}` {
		t.Fatalf("first candidate = %q", candidates[0])
	}
}

func TestParseWorkerResultRecoversFromDecoyObject(t *testing.T) {
	raw := []byte(`I finished the task. Config used: {"mode": "fast"}.

Final envelope:
{"schema_version":1,"status":"completed","summary":"implemented the fix","files_changed":["main.go"],"checks_run":[],"remaining_risks":[]}`)
	result, err := parseWorkerResult(raw)
	if err != nil {
		t.Fatalf("parseWorkerResult() error = %v", err)
	}
	if result.Summary != "implemented the fix" || len(result.FilesChanged) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseWorkerResultRecoversFencedJSON(t *testing.T) {
	raw := []byte("Done.\n```json\n{\"schema_version\":1,\"status\":\"completed\",\"summary\":\"done\",\"files_changed\":[],\"checks_run\":[],\"remaining_risks\":[]}\n```")
	result, err := parseWorkerResult(raw)
	if err != nil {
		t.Fatalf("parseWorkerResult() error = %v", err)
	}
	if result.Summary != "done" {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestParseWorkerResultKeepsValidationErrorWhenNoCandidateMatches(t *testing.T) {
	raw := []byte(`{"schema_version":1,"status":"completed","summary":"","files_changed":[],"checks_run":[],"remaining_risks":[]}`)
	_, err := parseWorkerResult(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsOutputContractError(err) || !strings.Contains(err.Error(), "worker summary is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseWorkerResultRecoversAfterUnmatchedBrace(t *testing.T) {
	raw := []byte(`First replace {name with the user value.

{"schema_version":1,"status":"completed","summary":"replaced placeholder","files_changed":[],"checks_run":[],"remaining_risks":[]}`)
	result, err := parseWorkerResult(raw)
	if err != nil {
		t.Fatalf("parseWorkerResult() error = %v", err)
	}
	if result.Summary != "replaced placeholder" {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestParseReviewPayloadSkipsInvalidCandidates(t *testing.T) {
	raw := []byte(`Review notes reference {"file": "main.go"} and conclude:
{"schema_version":1,"verdict":"pass","summary":"looks good","findings":[],"test_suggestions":[]}`)
	review, err := parseReviewPayload(raw)
	if err != nil {
		t.Fatalf("parseReviewPayload() error = %v", err)
	}
	if review.Verdict != "pass" {
		t.Fatalf("review = %#v", review)
	}
}

func TestDecodeEmbeddedJSONReturnsBaseErrorWhenNothingMatches(t *testing.T) {
	raw := []byte("prose only, no payload")
	calls := 0
	err := decodeEmbeddedJSON(raw, func([]byte) error {
		calls++
		return errors.New("no match")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected a single decode attempt for prose without objects, got %d", calls)
	}
}

func TestGrokBuildCmdCoder(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &GrokAdapter{DefaultModel: "grok-4.5", ReasoningEffort: "high", ExtraArgs: []string{"--verbatim"}}
	spec, err := adapter.BuildCmd(RoleCoder, Request{Prompt: "make it work", Workdir: "/repo", SchemaPath: schemaPath})
	if err != nil {
		t.Fatalf("BuildCmd() error = %v", err)
	}
	want := []string{
		"grok", "--single", "make it work", "--cwd", "/repo",
		"--model", "grok-4.5", "--reasoning-effort", "high",
		"--output-format", "json", "--no-plan", "--no-subagents", "--no-memory",
		"--permission-mode", "acceptEdits",
		"--json-schema", `{"type":"object"}`, "--verbatim",
	}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Fatalf("argv mismatch\nwant: %#v\ngot:  %#v", want, spec.Argv)
	}
	if len(spec.Stdin) != 0 {
		t.Fatalf("stdin = %q, want empty", string(spec.Stdin))
	}
}

func TestGrokBuildCmdAdversaryAndScoutReadOnly(t *testing.T) {
	schemaPath := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &GrokAdapter{DefaultModel: "grok-4.5", ReasoningEffort: "high"}
	for _, role := range []Role{RoleAdversary, RoleSupervisor, RoleReporter, RoleScout} {
		spec, err := adapter.BuildCmd(role, Request{Prompt: "inspect", Workdir: "/repo", SchemaPath: schemaPath})
		if err != nil {
			t.Fatalf("BuildCmd(%s) error = %v", role, err)
		}
		want := []string{
			"grok", "--single", "inspect", "--cwd", "/repo",
			"--model", "grok-4.5", "--reasoning-effort", "high",
			"--output-format", "json", "--no-plan", "--no-subagents", "--no-memory",
			"--permission-mode", "dontAsk",
			"--tools", "read_file,list_dir",
		}
		if role != RoleScout && role != RoleReporter {
			want = append(want, "--json-schema", `{"type":"object"}`)
		}
		if !reflect.DeepEqual(spec.Argv, want) {
			t.Fatalf("%s argv mismatch\nwant: %#v\ngot:  %#v", role, want, spec.Argv)
		}
	}
}

func TestGrokBuildCmdRejectsUnknownRole(t *testing.T) {
	_, err := (&GrokAdapter{}).BuildCmd(Role("invalid"), Request{Prompt: "x", Workdir: "/repo"})
	if err == nil || !strings.Contains(err.Error(), `unsupported role "invalid"`) {
		t.Fatalf("BuildCmd() error = %v", err)
	}
}

func TestGrokParseResult(t *testing.T) {
	adapter := &GrokAdapter{}
	textResult, err := adapter.ParseResult(RoleCoder, []byte("implemented"))
	if err != nil || textResult.Text != "implemented" {
		t.Fatalf("coder result = %#v, error = %v", textResult, err)
	}
	reviewResult, err := adapter.ParseResult(RoleAdversary, []byte(`{"verdict":"pass","summary":"looks good","findings":[],"test_suggestions":[]}`))
	if err != nil || reviewResult.Review == nil || reviewResult.Review.Summary != "looks good" {
		t.Fatalf("review result = %#v, error = %v", reviewResult, err)
	}
	scoutResult, err := adapter.ParseResult(RoleScout, []byte(`{"summary":"found it","relevant_files":["main.go"]}`))
	if err != nil || scoutResult.Scout == nil || scoutResult.Scout.Summary != "found it" {
		t.Fatalf("scout result = %#v, error = %v", scoutResult, err)
	}
}

func TestGrokDetectNonZeroExit(t *testing.T) {
	oldLookPath := execLookPath
	oldCommandContext := execCommandContext
	defer func() {
		execLookPath = oldLookPath
		execCommandContext = oldCommandContext
	}()
	execLookPath = func(file string) (string, error) {
		if file == "grok" {
			return "/mock/bin/grok", nil
		}
		return "", os.ErrNotExist
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "grok" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, name, args...)
	}
	info, err := (&GrokAdapter{}).Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !info.Found || info.Runnable || !strings.Contains(info.Hint, "probe failed") {
		t.Fatalf("unexpected detection result: %#v", info)
	}
}
