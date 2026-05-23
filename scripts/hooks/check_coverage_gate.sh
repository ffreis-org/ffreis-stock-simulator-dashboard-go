#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}/check_required_tools.sh" go >/dev/null

coverage_min="${COVERAGE_MIN:-90}"
profile_file="$(mktemp)"
trap 'rm -f "${profile_file}"' EXIT

go test ./... -coverprofile="${profile_file}"

total_line="$(go tool cover -func="${profile_file}" | awk '/^total:/ {print $0}')"
if [ -z "${total_line}" ]; then
  echo "Unable to determine total coverage from ${profile_file}." >&2
  exit 1
fi

total_pct="$(awk '/^total:/ {gsub("%", "", $3); print $3}' <<<"${total_line}")"

if awk -v total="${total_pct}" -v min="${coverage_min}" 'BEGIN { exit !(total+0 >= min+0) }'; then
  echo "Coverage gate passed: ${total_pct}% >= ${coverage_min}%"
  exit 0
fi

echo "Coverage gate failed: ${total_pct}% < ${coverage_min}%." >&2
echo "Add tests or lower COVERAGE_MIN explicitly for non-protected runs." >&2
exit 1
