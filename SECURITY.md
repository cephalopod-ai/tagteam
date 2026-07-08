# Security Policy

## Reporting

If you find a security issue in `tagteam`, please do not open a public issue with exploit details.

Report it privately to the maintainer with:

- a description of the issue;
- affected version or commit;
- reproduction steps or proof of concept;
- impact assessment.

If no private reporting channel is published yet, open a minimal public issue asking for a contact path without disclosing the exploit.

## Scope

Security issues may include:

- command injection or unsafe shell execution;
- prompt or artifact handling that can cause unintended destructive actions;
- secret leakage in run artifacts or logs;
- sandbox or permission-boundary bypasses;
- unsafe adapter behavior that causes execution outside the intended workdir.

## Expectations

`tagteam` orchestrates third-party agent CLIs. Security fixes may require coordinated changes across prompt construction, adapter argv construction, run artifact handling, and preflight validation.
