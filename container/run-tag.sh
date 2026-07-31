#!/usr/bin/env bash
set -euo pipefail

runtime_environment=/run/secrets/tos-tag-runtime.env
if [[ ! -f "${runtime_environment}" ]]; then
  echo "runtime.env is required; copy runtime.env.example and fill only the selected live values" >&2
  exit 1
fi
if [[ ! -f go.mod ]]; then
  echo "persistent workspace is not bootstrapped; run bootstrap-workspace first" >&2
  exit 1
fi

set -a
# runtime.env is mounted read-only and sourced inside the process so Compose
# config/inspect output never expands its secret values.
source "${runtime_environment}"
set +a

export TAG__HTTP__ADDR=0.0.0.0:8090
export TAG__MONGO__URI=mongodb://mongo:27017
export TAG__LOGGING__FILE_PATH=/workspace/state/logs/tos-tag.jsonl
export TAG__OPENCODE__COMMAND=/usr/local/bin/opencode
export TAG__OPENCODE__WORKER_ROOT=/workspace/state/workers
export TAG__MARKETPLACES__HEADLESS_ROOT=/workspace/skills/telemetryos-agent-skills
export TAG__MARKETPLACES__BASE_ROOT=/workspace/skills/tag-agent-skills

exec go run ./cmd/api
