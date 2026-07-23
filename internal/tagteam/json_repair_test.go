package tagteam

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepairJSONWithWorkerPrefersParsedTextOverProviderEnvelope(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(repo, "README.md"), "baseline\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "baseline")

	runDir := t.TempDir()
	artifactBase := filepath.Join(runDir, "supervisor-round-1.json")
	text := "```json\n{\"schema_version\":1,\"verdict\":\"pass\",\"summary\":\"repaired\",\"findings\":[],\"test_suggestions\":[]}\n```"
	providerEnvelope, err := json.Marshal(map[string]any{"text": text, "structuredOutput": nil})
	if err != nil {
		t.Fatal(err)
	}
	worker := fakeDirectAdapter{
		build: func(role Role, req Request) (*CommandSpec, error) {
			return &CommandSpec{Argv: []string{"fake"}, Dir: repo, Output: req.OutputPath}, nil
		},
		direct: func(role Role, req Request) (Result, error) {
			return Result{Raw: providerEnvelope, Text: text}, nil
		},
	}

	repaired, _, attempted, err := NewApp(DefaultConfig()).repairJSONWithWorker(context.Background(), RunOptions{
		JSONRepair:     "worker",
		Coder:          RoleTarget{Adapter: "fake-direct", Model: "repair"},
		Workdir:        repo,
		Timeout:        2 * time.Second,
		MaxOutputBytes: 1024 * 1024,
	}, map[string]Adapter{"fake-direct": worker}, runDir, artifactBase, "review", ReviewSchema, []byte(`{`), context.Canceled)
	if err != nil {
		t.Fatalf("repairJSONWithWorker() error = %v", err)
	}
	if !attempted {
		t.Fatal("expected JSON repair attempt")
	}
	if string(repaired) != text {
		t.Fatalf("repaired = %q, want parsed text %q", repaired, text)
	}
	if strings.Contains(string(repaired), "structuredOutput") {
		t.Fatalf("repair retained provider envelope: %q", repaired)
	}
	review, err := parseReviewPayload(repaired)
	if err != nil || review.Verdict != "pass" {
		t.Fatalf("repaired review = %#v, error = %v", review, err)
	}
	persisted, err := os.ReadFile(artifactBase + ".repaired.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != text {
		t.Fatalf("persisted repair = %q, want parsed model text", persisted)
	}
}
