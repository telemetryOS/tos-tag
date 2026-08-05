#!/usr/bin/env bash
set -euo pipefail
umask 077

script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
install_root="${TAG_CODE_SEMANTIC_ROOT:-${HOME}/.local/lib/tos-tag-semantic}"
semble_bin="${TAG_CODE_SEMBLE_BIN:-${HOME}/.local/bin/semble}"
semble_version=0.5.3
model_revision=e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b
release_id="semble-${semble_version}-model-${model_revision}"
release_root="${install_root}/releases/${release_id}"
installed_version=""

command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 1; }
mkdir -p "${install_root}/releases" "$(dirname "${semble_bin}")"
if [[ -x "${release_root}/venv/bin/semble" ]]; then
  installed_version="$("${release_root}/venv/bin/semble" --version 2>/dev/null || true)"
fi

if [[ "${installed_version}" != "${semble_version}" || ! -f "${release_root}/model/.tos-tag-model-revision" ]]; then
  rm -rf "${release_root}"
  mkdir -p "${release_root}"
  trap 'rm -rf "${release_root}"' EXIT
  python3 -m venv "${release_root}/venv"
  "${release_root}/venv/bin/python" -m pip install --disable-pip-version-check --no-deps \
    --requirement "${script_root}/requirements-semantic-search.txt"
  "${release_root}/venv/bin/python" -c \
    'from huggingface_hub import snapshot_download; import sys; snapshot_download(repo_id="minishlab/potion-code-16M-v2", revision=sys.argv[1], local_dir=sys.argv[2])' \
    "${model_revision}" "${release_root}/model"
  rm -rf "${release_root}/model/.cache"
  (
    cd "${release_root}/model"
    sha256sum --check --strict "${script_root}/semantic-model.sha256"
  )
  printf '%s\n' "${model_revision}" >"${release_root}/model/.tos-tag-model-revision"
  [[ "$("${release_root}/venv/bin/semble" --version)" == "${semble_version}" ]] || {
    echo "installed Semble version does not match ${semble_version}" >&2
    exit 1
  }
  "${release_root}/venv/bin/python" -c 'from model2vec import StaticModel; import sys; StaticModel.from_pretrained(sys.argv[1])' "${release_root}/model"
  trap - EXIT
fi

ln -sfn "${release_root}/venv/bin/semble" "${semble_bin}"
printf 'Semble %s installed with model revision %s\n' "${semble_version}" "${model_revision}"
printf 'model path: %s\n' "${release_root}/model"
