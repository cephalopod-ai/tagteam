package tagteam

import (
	"encoding/json"
	"fmt"
)

func BuildRelaySupervisorInstructionsPrompt(userPrompt, brief string, scout Scout, baseline *TestRun) string {
	scoutJSON := CompactScoutForPrompt(scout)
	return fmt.Sprintf(`You are the supervisor in a three-agent relay workflow.
You are read-only. Do not edit files. Do not reveal hidden chain-of-thought;
capture only public rationale: assumptions, decisions, risks, and checks.

Original request:
%s

Initial supervisor brief:
%s

Scout reconnaissance JSON:
%s

%s

Condense this into final worker instructions for the coder: concrete files
or areas to inspect, implementation approach, edge cases, and verification.
Keep it concise and actionable.`, userPrompt, brief, scoutJSON, hostBaselineEvidence(baseline))
}

func BuildRelayCoderPrompt(workdir, userPrompt, brief, scoutInstructions string, scout Scout) string {
	scoutJSON := CompactScoutForPrompt(scout)
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
checks run, known remaining risk.`, workdir, userPrompt, brief, scoutJSON, scoutInstructions)
}

func BuildRelayFixPrompt(round int, userPrompt, diff, brief, scoutInstructions string, scout Scout, postScout Scout, review Review) string {
	scoutJSON := CompactScoutForPrompt(scout)
	postScoutJSON := CompactScoutForPrompt(postScout)
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

%s

Fix the findings, keep the original request satisfied, avoid unrelated
changes, update tests as needed. Finish with: fixes made, checks run,
any finding you dispute and why.`, round, userPrompt, brief, scoutJSON, postScoutJSON, scoutInstructions, string(findingsJSON), diff, untrustedArtifactNotice)
}

func BuildRelaySupervisorReviewPrompt(userPrompt, baseline, brief string, scout Scout, postScout Scout, scoutInstructions, diffRef, testOutput string, diffViaStdin bool) string {
	diffSection := diffRef
	if diffViaStdin {
		diffSection = "(diff provided via stdin)"
	}
	scoutJSON := CompactScoutForPrompt(scout)
	postScoutJSON := CompactScoutForPrompt(postScout)
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

%s

%s

%s

Evaluate: does the diff satisfy the request; correctness bugs; missed
edge cases; missing tests for changed behavior; unrelated modifications;
security/data-loss/migration risk; consistency with repo patterns.

Scout findings are advisory only. Use them as context, but only your
supervisor review can produce blocking findings. Reserve
prior_finding_dispositions for host-provided IDs in the prior-findings context;
do not emit dispositions for scout items or any other advisory observation.

Respond with JSON matching the provided schema. Use "pass" only when
there are no blocker or major findings. Every finding must name a file
	and a concrete fix.`, userPrompt, brief, scoutJSON, postScoutJSON, scoutInstructions, baseline, diffSection, testOutput, untrustedArtifactNotice, reviewerCurrentStateDiscipline, reviewerFindingGrounding)
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

%s

Report, concisely:
- What remains incomplete or risky.
- Which findings you agree with, dispute, or could not verify.
- What you would do next if the human chose to continue.
- Whether you believe the current diff is acceptable as-is.`, roleLabel, modeName, counterpartLabel, userPrompt, string(findingsJSON), diff, string(testsJSON), untrustedArtifactNotice)
}
