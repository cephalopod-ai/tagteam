package tagteam

import (
	"errors"
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
