# ENG-5958 implementation — tos-tag

## What changed
- go.mod → `go 1.27.1` (+ tidy / sum updates as needed)
- Dockerfile golang base image pins updated when present
- Repo-specific fixes only when verify failed (see fleet trail)

## Local Fleet Gate
Substitution: fleet-wide verify gate already green for all 39 in-scope repos (`results.tsv` PASS). Targeted package retests for Auth/Devices/Apps after shared-infra fixes.

## Visual Evidence Gate
N/A — no browser-visible surface.
