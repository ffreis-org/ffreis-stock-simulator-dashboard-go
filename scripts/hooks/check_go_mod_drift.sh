#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

source "$(dirname "$0")/../lib/common.sh"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}/check_required_tools.sh" go >/dev/null

if [ ! -f go.mod ]; then
  common_info "No go.mod found in $(pwd); skipping go mod drift check."
  exit 0
fi

tmp_dir="$(mktemp -d)"
had_go_sum=0
if [ -f go.sum ]; then
  had_go_sum=1
fi

restore_files() {
  cp "${tmp_dir}/go.mod" go.mod
  if [ "${had_go_sum}" -eq 1 ]; then
    cp "${tmp_dir}/go.sum" go.sum
  else
    rm -f go.sum
  fi
}

trap 'restore_files; rm -rf "${tmp_dir}"' EXIT

cp go.mod "${tmp_dir}/go.mod"
if [ "${had_go_sum}" -eq 1 ]; then
  cp go.sum "${tmp_dir}/go.sum"
fi

if ! go mod tidy >/dev/null 2>&1; then
  common_err "go mod tidy failed; cannot verify go.mod/go.sum drift."
  exit 1
fi

changed=0
if ! cmp -s go.mod "${tmp_dir}/go.mod"; then
  changed=1
fi

if [ "${had_go_sum}" -eq 1 ]; then
  if [ ! -f go.sum ] || ! cmp -s go.sum "${tmp_dir}/go.sum"; then
    changed=1
  fi
else
  if [ -f go.sum ]; then
    changed=1
  fi
fi

if [ "${changed}" -ne 0 ]; then
  common_err "go.mod/go.sum drift detected."
  common_err "Run 'go mod tidy' and commit the resulting go.mod/go.sum changes."
  exit 1
fi
