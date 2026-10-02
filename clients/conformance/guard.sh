#!/usr/bin/env bash
# The must-have-run guard. A conformance suite that SKIPS (no Postgres, wrong
# env, a filter that matched nothing) exits 0 and reads as green. This reads
# the runner's machine output and fails unless EVERY case in cases.txt ran
# and passed, so a missing case is a red job, not a quiet one.
#
#   guard.sh go <go test -json output>
#   guard.sh ts <vitest --reporter=json output>
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
lang="${1:?usage: guard.sh go|ts <results file>}"
results="${2:?usage: guard.sh go|ts <results file>}"
[ -s "$results" ] || { echo "guard: $results is empty: the suite did not run" >&2; exit 1; }

missing=0
total=0
while IFS= read -r case_name; do
  [ -z "$case_name" ] && continue
  total=$((total + 1))
  case "$lang" in
    go)
      grep -qF "\"Action\":\"pass\"" <(grep -F "\"Test\":\"TestConformance/${case_name}\"" "$results") || {
        echo "guard: case not run or not passed: $case_name" >&2
        missing=$((missing + 1))
      }
      ;;
    ts)
      node -e '
        const r = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"));
        const ok = r.testResults.some((f) =>
          f.assertionResults.some((a) => a.title === process.argv[2] && a.status === "passed"));
        process.exit(ok ? 0 : 1);
      ' "$results" "$case_name" || {
        echo "guard: case not run or not passed: $case_name" >&2
        missing=$((missing + 1))
      }
      ;;
    *) echo "guard: unknown language $lang" >&2; exit 2 ;;
  esac
done <"$here/cases.txt"

if [ "$missing" -ne 0 ]; then
  echo "guard: $missing of $total conformance cases did not pass ($lang)" >&2
  exit 1
fi
echo "guard: all $total conformance cases ran and passed ($lang)"
