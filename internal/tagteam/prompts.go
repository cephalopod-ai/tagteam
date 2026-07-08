package tagteam

import (
	"encoding/json"
	"fmt"
	"strings"
)

const coderSystemPrompt = `You are the coder in a two-agent adversarial workflow. An independent adversarial reviewer will inspect your diff; it cannot edit files.

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.

Finish with a concise summary: files changed, behavior changed, checks run, known remaining risk.`

const soloSystemPrompt = `You are the implementation agent in a solo tagteam run. There is no reviewer, supervisor, or adversary in this run.

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.

Finish with a concise summary: files changed, behavior changed, checks run, known remaining risk.`

const workerSystemPrompt = `You are the worker in a supervisor-worker coding workflow. A supervisor writes a compact implementation brief before you start, then reviews your diff; it does not edit files by default.

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.

Finish with a concise summary: files changed, behavior changed, checks run, known remaining risk.`

const repoInstructionsPromptHeader = `Repository Instructions (follow unless they conflict with the user's explicit request or role safety constraints):`

func withRepoInstructions(prompt, repoInstructions string) string {
	repoInstructions = strings.TrimSpace(repoInstructions)
	if repoInstructions == "" {
		return prompt
	}
	return strings.TrimSpace(prompt) + "\n\n" + repoInstructionsPromptHeader + "\n\n" + repoInstructions
}

func BuildSoloPrompt(workdir, userPrompt string) string {
	return fmt.Sprintf(`%s

Complete this request in the repository at %s:

%s`, soloSystemPrompt, workdir, userPrompt)
}

func BuildCoderPrompt(workdir, userPrompt string) string {
	return fmt.Sprintf(`You are the coder in a two-agent adversarial workflow. An independent
adversarial reviewer will inspect your diff; it cannot edit files.

Complete this request in the repository at %s:

%s

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.

Finish with a concise summary: files changed, behavior changed,
checks run, known remaining risk.`, workdir, userPrompt)
}

func BuildAdversaryPrompt(userPrompt, baseline, diffRef, testOutput string, diffViaStdin bool) string {
	diffSection := diffRef
	if diffViaStdin {
		diffSection = "(diff provided via stdin)"
	}
	return fmt.Sprintf(`You are the adversarial reviewer in a two-agent coding workflow.
You cannot edit files. Do not propose broad refactors unless required
for correctness.

Original request:
%s

Diff under review (vs baseline %s):
%s

Test output:
%s

Evaluate: does the diff satisfy the request; correctness bugs; missed
edge cases; missing tests for changed behavior; unrelated modifications;
security/data-loss/migration risk; consistency with repo patterns.

Respond with JSON matching the provided schema. Use "pass" only when
there are no blocker or major findings. Every finding must name a file
and a concrete fix.`, userPrompt, baseline, diffSection, testOutput)
}

func BuildFixPrompt(round int, userPrompt, diff string, review Review) string {
	findingsJSON, _ := json.MarshalIndent(review, "", "  ")
	return fmt.Sprintf(`You are the coder in round %d. The adversarial reviewer found issues
with your previous change.

Original request:
%s

Reviewer findings (fix all blocker and major items):
%s

Current diff vs baseline:
%s

Fix the findings, keep the original request satisfied, avoid unrelated
changes, update tests as needed. Finish with: fixes made, checks run,
any finding you dispute and why.`, round, userPrompt, string(findingsJSON), diff)
}

func BuildSupervisorBriefPrompt(workdir, userPrompt string, canEdit bool) string {
	editNote := "You do not edit files yourself; the worker will implement the change."
	if canEdit {
		editNote = "You may make small exploratory edits if needed, but the worker does the primary implementation."
	}
	return fmt.Sprintf(`You are the supervisor in a supervisor-worker coding workflow. Before any
code is written, produce a compact implementation brief for the worker who
will make the change. %s

Repository: %s

Request:
%s

Write a compact implementation brief: the concrete approach, the files or
areas likely involved, edge cases to handle, and how to verify the change.
Do not write the diff yourself. Keep it short and actionable.`, editNote, workdir, userPrompt)
}

func BuildSupervisorWorkPlanPrompt(workdir, userPrompt string, maxPackages int, requestedPackage string) string {
	if maxPackages <= 0 {
		maxPackages = 5
	}
	packageInstruction := "Select package P1 for this run."
	if strings.TrimSpace(requestedPackage) != "" {
		packageInstruction = fmt.Sprintf("Select package %q for this run if it exists; otherwise select the first package and explain the mismatch in defer.", requestedPackage)
	}
	return fmt.Sprintf(`You are the supervisor in a supervisor-worker coding workflow.
Before any code is written, triage the user's request into small, ordered
implementation work packages. You are read-only. Do not edit files.

Repository: %s

Request:
%s

Classify scope, split the work into 1-%d ordered packages, and choose exactly
one package for this run. %s

Return JSON only, with this shape:
{
  "summary": "One sentence summary of the selected package.",
  "packages": [
    {
      "id": "P1",
      "title": "Short package title",
      "goal": "Concrete outcome for this package only.",
      "allowed_scope": ["path/or/area"],
      "acceptance": ["specific pass condition"],
      "validation": ["specific command or check"]
    }
  ],
  "selected_package": "P1",
  "defer": ["work intentionally left for later packages"]
}

Rules:
- Keep packages small enough to review as separate patches.
- Put the safest or most foundational package first.
- Do not include hidden chain-of-thought; use concise public rationale only.
- Do not ask the worker to implement packages other than selected_package.`, workdir, userPrompt, maxPackages, packageInstruction)
}

func BuildWorkPackageBrief(plan WorkPlan, pkg WorkPackage) string {
	pkgJSON, _ := json.MarshalIndent(pkg, "", "  ")
	deferJSON, _ := json.MarshalIndent(plan.Defer, "", "  ")
	return fmt.Sprintf(`Supervisor work package:
%s

Deferred work not in scope for this run:
%s

Implement only the selected package. Treat deferred packages as out of scope
unless they are strictly necessary for the selected package to compile or pass
its acceptance criteria.`, string(pkgJSON), string(deferJSON))
}

func BuildWorkerImplementPrompt(workdir, userPrompt, brief string) string {
	return fmt.Sprintf(`You are the worker in a supervisor-worker coding workflow. A supervisor
will review your diff; it does not edit files by default.

Complete this request in the repository at %s:

%s

Supervisor's implementation brief:
%s

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.

Finish with a concise summary: files changed, behavior changed,
checks run, known remaining risk.`, workdir, userPrompt, brief)
}

func BuildWorkerPackageImplementPrompt(workdir, userPrompt string, plan WorkPlan, pkg WorkPackage) string {
	return BuildWorkerImplementPrompt(workdir, fmt.Sprintf(`Original request:
%s

Selected work package:
%s`, userPrompt, BuildWorkPackageBrief(plan, pkg)), "Implement only the selected work package. Do not implement deferred packages.")
}

func BuildWorkerFixPrompt(round int, userPrompt, diff string, review Review) string {
	findingsJSON, _ := json.MarshalIndent(review, "", "  ")
	return fmt.Sprintf(`You are the worker in round %d. The supervisor found issues with your
previous change.

Original request:
%s

Supervisor findings (fix all blocker and major items):
%s

Current diff vs baseline:
%s

Fix the findings, keep the original request satisfied, avoid unrelated
changes, update tests as needed. Finish with: fixes made, checks run,
any finding you dispute and why.`, round, userPrompt, string(findingsJSON), diff)
}

func BuildWorkerPackageFixPrompt(round int, userPrompt, diff string, pkg WorkPackage, review Review) string {
	findingsJSON, _ := json.MarshalIndent(review, "", "  ")
	pkgJSON, _ := json.MarshalIndent(pkg, "", "  ")
	return fmt.Sprintf(`You are the worker in round %d. The supervisor found
issues with the selected work package.

Original request:
%s

Selected work package:
%s

Supervisor findings (fix all blocker and major items for this package):
%s

Current diff vs baseline:
%s

Fix the findings, keep the selected package satisfied, avoid unrelated
changes, and do not implement deferred packages unless strictly necessary
for this package to compile or pass its acceptance criteria. Update tests as
needed. Finish with: fixes made, checks run, any finding you dispute and why.`, round, userPrompt, string(pkgJSON), string(findingsJSON), diff)
}

func BuildSupervisorReviewPrompt(userPrompt, baseline, diffRef, testOutput string, diffViaStdin bool) string {
	diffSection := diffRef
	if diffViaStdin {
		diffSection = "(diff provided via stdin)"
	}
	return fmt.Sprintf(`You are the supervisor reviewing the worker's diff in a supervisor-worker
coding workflow. You cannot edit files. Do not propose broad refactors
unless required for correctness.

Original request:
%s

Diff under review (vs baseline %s):
%s

Test output:
%s

Evaluate: does the diff satisfy the request; correctness bugs; missed
edge cases; missing tests for changed behavior; unrelated modifications;
security/data-loss/migration risk; consistency with repo patterns.

Respond with JSON matching the provided schema. Use "pass" only when
there are no blocker or major findings. Every finding must name a file
and a concrete fix.`, userPrompt, baseline, diffSection, testOutput)
}

func BuildSupervisorPackageReviewPrompt(userPrompt string, plan WorkPlan, pkg WorkPackage, baseline, diffRef, testOutput string, diffViaStdin bool) string {
	diffSection := diffRef
	if diffViaStdin {
		diffSection = "(diff provided via stdin)"
	}
	planJSON, _ := json.MarshalIndent(plan, "", "  ")
	pkgJSON, _ := json.MarshalIndent(pkg, "", "  ")
	return fmt.Sprintf(`You are the supervisor reviewing the worker's diff in a
supervisor-worker coding workflow. You cannot edit files.

Original request:
%s

Selected work package under review:
%s

Full work plan:
%s

Diff under review (vs baseline %s):
%s

Test output:
%s

Evaluate only the selected work package. Do not fail the run for deferred
packages unless the worker's diff makes them harder, breaks existing behavior,
or violates the selected package acceptance criteria.

Respond with JSON matching the provided schema. Use "pass" only when
there are no blocker or major findings for the selected package. Every
finding must name a file and a concrete fix.`, userPrompt, string(pkgJSON), string(planJSON), baseline, diffSection, testOutput)
}

func BuildScoutPrompt(workdir, userPrompt, brief, mode, phase, diff, testOutput string) string {
	if strings.TrimSpace(mode) == "" {
		mode = "recon"
	}
	modeInstructions := scoutModeInstructions(mode)
	diffSection := "(not available for this scout phase)"
	if strings.TrimSpace(diff) != "" {
		diffSection = diff
	}
	testSection := "(no tests run before this scout phase)"
	if strings.TrimSpace(testOutput) != "" {
		testSection = testOutput
	}
	return fmt.Sprintf(`You are the scout in a three-agent relay workflow.
You are read-only. Do not edit files. Do not reveal hidden chain-of-thought;
capture only public rationale: assumptions, decisions, risks, and checks.

Scout phase: %s
Scout mode: %s

Repository: %s

Original request:
%s

Supervisor brief:
%s

Current diff context:
%s

Test output:
%s

%s

Scout findings are advisory only and must not directly block the run.
Keep values concise and specific.`, phase, mode, workdir, userPrompt, brief, diffSection, testSection, modeInstructions)
}

func scoutModeInstructions(mode string) string {
	switch mode {
	case "lint":
		return `Perform a read-only static review for obvious defects, dead code, style drift, naming inconsistency, missing error handling, and duplicated logic.
Return JSON:
{
  "mode": "lint",
  "summary": "One concise summary.",
  "items": [{"severity":"minor|nit","file":"path","line":123,"issue":"specific issue","suggestion":"specific suggestion"}],
  "do_not_block": true
}`
	case "polish":
		return `Look for small cleanup opportunities after implementation. Do not propose architecture rewrites.
Return JSON:
{
  "mode": "polish",
  "summary": "Small cleanup opportunities after the implementation.",
  "items": [{"severity":"minor|nit","file":"path","line":123,"issue":"specific issue","suggestion":"specific suggestion"}],
  "do_not_block": true
}`
	case "tests":
		return `Suggest missing targeted tests and edge cases. Focus on changed behavior and likely regressions.
Return JSON:
{
  "mode": "tests",
  "summary": "Targeted test opportunities.",
  "items": [{"severity":"minor|nit","file":"path","line":123,"issue":"missing coverage or edge case","suggestion":"specific test"}],
  "do_not_block": true
}`
	case "risk":
		return `Review security, data-loss, migration, backwards-compatibility, and operational risks.
Return JSON:
{
  "mode": "risk",
  "summary": "Concrete risk review.",
  "items": [{"severity":"minor|nit","file":"path","line":123,"issue":"specific risk","suggestion":"specific mitigation"}],
  "do_not_block": true
}`
	default:
		return `Map files, entry points, existing patterns, risks, and likely tests.
Return JSON:
{
  "mode": "recon",
  "summary": "Concise reconnaissance summary.",
  "relevant_files": ["path"],
  "likely_entry_points": ["concise entry point"],
  "existing_patterns": ["pattern to follow"],
  "risks": ["concrete risk"],
  "suggested_tests": ["specific check"],
  "do_not_block": true
}`
	}
}

func BuildRelaySupervisorInstructionsPrompt(userPrompt, brief string, scout Scout) string {
	scoutJSON, _ := json.MarshalIndent(scout, "", "  ")
	return fmt.Sprintf(`You are the supervisor in a three-agent relay workflow.
You are read-only. Do not edit files. Do not reveal hidden chain-of-thought;
capture only public rationale: assumptions, decisions, risks, and checks.

Original request:
%s

Initial supervisor brief:
%s

Scout reconnaissance JSON:
%s

Condense this into final worker instructions for the coder: concrete files
or areas to inspect, implementation approach, edge cases, and verification.
Keep it concise and actionable.`, userPrompt, brief, string(scoutJSON))
}

func BuildRelayCoderPrompt(workdir, userPrompt, brief, scoutInstructions string, scout Scout) string {
	scoutJSON, _ := json.MarshalIndent(scout, "", "  ")
	return fmt.Sprintf(`You are the coder in a three-agent relay workflow.
A scout has performed read-only reconnaissance and a supervisor has condensed
that into implementation instructions. A supervisor will review your diff.

Complete this request in the repository at %s:

%s

Supervisor brief:
%s

Scout reconnaissance JSON:
%s

Final worker instructions:
%s

Rules:
- Edit files directly. Do not describe a plan instead of implementing.
- Make the smallest correct change that satisfies the request.
- Follow the repository's existing style and architecture.
- Add or update tests when behavior changes.
- Leave unrelated files alone.
- Do not reveal hidden chain-of-thought; summarize public assumptions,
  decisions, risks, and checks only.

Finish with a concise summary: files changed, behavior changed,
checks run, known remaining risk.`, workdir, userPrompt, brief, string(scoutJSON), scoutInstructions)
}

func BuildRelayFixPrompt(round int, userPrompt, diff, brief, scoutInstructions string, scout Scout, postScout Scout, review Review) string {
	scoutJSON, _ := json.MarshalIndent(scout, "", "  ")
	postScoutJSON, _ := json.MarshalIndent(postScout, "", "  ")
	findingsJSON, _ := json.MarshalIndent(review, "", "  ")
	return fmt.Sprintf(`You are the coder in relay round %d. The supervisor
found issues with your previous change.

Original request:
%s

Supervisor brief:
%s

Scout reconnaissance JSON:
%s

Latest post-scout advisory JSON:
%s

Final worker instructions:
%s

Supervisor findings (fix all blocker and major items):
%s

Current diff vs baseline:
%s

Fix the findings, keep the original request satisfied, avoid unrelated
changes, update tests as needed. Finish with: fixes made, checks run,
any finding you dispute and why.`, round, userPrompt, brief, string(scoutJSON), string(postScoutJSON), scoutInstructions, string(findingsJSON), diff)
}

func BuildRelaySupervisorReviewPrompt(userPrompt, baseline, brief string, scout Scout, postScout Scout, scoutInstructions, diffRef, testOutput string, diffViaStdin bool) string {
	diffSection := diffRef
	if diffViaStdin {
		diffSection = "(diff provided via stdin)"
	}
	scoutJSON, _ := json.MarshalIndent(scout, "", "  ")
	postScoutJSON, _ := json.MarshalIndent(postScout, "", "  ")
	return fmt.Sprintf(`You are the supervisor reviewing the coder's diff in
a three-agent relay workflow. You cannot edit files. Do not propose broad
refactors unless required for correctness.

Original request:
%s

Supervisor brief:
%s

Scout reconnaissance JSON:
%s

Post-scout advisory JSON:
%s

Final worker instructions:
%s

Diff under review (vs baseline %s):
%s

Test output:
%s

Evaluate: does the diff satisfy the request; correctness bugs; missed
edge cases; missing tests for changed behavior; unrelated modifications;
security/data-loss/migration risk; consistency with repo patterns.

Scout findings are advisory only. Use them as context, but only your
supervisor review can produce blocking findings.

Respond with JSON matching the provided schema. Use "pass" only when
there are no blocker or major findings. Every finding must name a file
and a concrete fix.`, userPrompt, brief, string(scoutJSON), string(postScoutJSON), scoutInstructions, baseline, diffSection, testOutput)
}

func BuildRoundLimitReportPrompt(roleLabel, counterpartLabel string, mode Mode, userPrompt, diff string, review Review, tests []TestRun) string {
	findingsJSON, _ := json.MarshalIndent(review, "", "  ")
	testsJSON, _ := json.MarshalIndent(tests, "", "  ")
	modeName := string(mode)
	if modeName == "" {
		modeName = string(ModeAdversarial)
	}
	return fmt.Sprintf(`You are the %s in a %s tagteam run.

The user-defined round limit has been reached. The run is done. Do not edit
files, do not request another round, and do not try to complete more work.
It is acceptable if the change is incomplete, if you disagree with the %s,
or if you would not call the result a pass; that disagreement is useful
signal for the human.

Original request:
%s

Latest review:
%s

Current diff vs baseline:
%s

Test history:
%s

Report, concisely:
- What remains incomplete or risky.
- Which findings you agree with, dispute, or could not verify.
- What you would do next if the human chose to continue.
- Whether you believe the current diff is acceptable as-is.`, roleLabel, modeName, counterpartLabel, userPrompt, string(findingsJSON), diff, string(testsJSON))
}
