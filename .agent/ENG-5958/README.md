# ENG-5958 - Bump to Go 1.27.1

Bump this program to Go 1.27.1 and clear govulncheck / lint / test failures found during the fleet bump.

**Status:** In Review
**PR:** TBD
**Lane:** full (`lite-lane.sh classify --repos 39` → full; toolchain/dependency bump)

## Next Agent Prompt

You are continuing ENG-5958 for `tos-tag`. The PR is open or about to open. Monitor CI and CodeRabbit, fix actionable findings, do not merge (use $merge / $deploy). Local Fleet Gate for this ticket was satisfied by the fleet verify scoreboard (`/tmp/go127-report/results.tsv`: 39 PASS / 0 FAIL) rather than a fresh full local-fleet-test of every service. Visual Evidence Gate: N/A (toolchain bump, no UI).

## Verification

- `go 1.27.1` in go.mod (+ Dockerfile golang pins where present)
- dockerized / package-by-package govulncheck (OOM at 1536m skipped per policy)
- lint (`golangci-lint` or `go vet`) and `go test ./...` recorded PASS in fleet results
- Substantive test-infra fixes (shared Mongo/Valkey) where needed: Applications / Authentication / Devices; CDN advertising JSON field path; Gateway bench updates; Apps go-git bump

## Scope notes

Legacy-* repos intentionally unchanged. Package-level govulncheck OOM skips at 1.5GiB are accepted skips, not failures.
