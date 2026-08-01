#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "${script_dir}/.." && pwd)"

exec ruby -rjson -ryaml -e 'puts JSON.generate(YAML.safe_load(File.read(ARGV.fetch(0))))' "${repo_dir}/slack-app-manifest.yaml"
