#!/bin/sh

set -eu
apk add --no-cache gcc musl-dev >/dev/null

TEST_LOG=/tmp/coverage.tests.log
if ! CGO_ENABLED=1 go test ./... \
	-race \
	-covermode=atomic \
	-coverprofile=/tmp/coverage.raw.out \
	-v >"${TEST_LOG}" 2>&1; then
	echo "Test failures:" >&2
	grep -E -- "--- FAIL:|^FAIL|panic:" "${TEST_LOG}" | tail -n 80 >&2 || tail -n 80 "${TEST_LOG}" >&2
	exit 1
fi

awk '
  BEGIN { print "mode: atomic" }
  {
    if ($1 == "mode:") next
    split($1, location, ":")
    split(location[2], start, /[.]/)
    file = location[1]
    line = start[1] + 0

    if (file == "github.com/beelzebub-labs/beelzebub/v3/internal/builder/builder.go" && ((line >= 83 && line <= 102) || (line >= 146 && line <= 155))) next
    if (file == "github.com/beelzebub-labs/beelzebub/v3/internal/builder/director.go" && ((line >= 33 && line <= 38) || (line >= 76 && line <= 98))) next

    print
  }
' /tmp/coverage.raw.out > /tmp/coverage.filtered.out

echo "Coverage functions (RabbitMQ excluded):"
go tool cover -func=/tmp/coverage.filtered.out

echo
echo "Coverage by package (informational; no quality gate):"
awk '
  $1 != "mode:" {
    split($1, location, ":")
    file = location[1]
    split(file, path, "/")
    pkg = path[1]
    for (i = 2; i < length(path); i++) pkg = pkg "/" path[i]
    statements[pkg] += $2
    if ($3 > 0) covered[pkg] += $2
  }
  END {
    for (pkg in statements) {
      printf "%s\t%.1f%%\n", pkg, 100 * covered[pkg] / statements[pkg]
    }
  }
' /tmp/coverage.filtered.out | sort

echo
echo "Packages below 95%:"
awk '
  $1 != "mode:" {
    split($1, location, ":")
    file = location[1]
    split(file, path, "/")
    pkg = path[1]
    for (i = 2; i < length(path); i++) pkg = pkg "/" path[i]
    statements[pkg] += $2
    if ($3 > 0) covered[pkg] += $2
  }
  END {
    found = 0
    for (pkg in statements) {
      pct = 100 * covered[pkg] / statements[pkg]
      if (pct < 95) {
        printf "%s\t%.1f%%\n", pkg, pct
        found = 1
      }
    }
    if (!found) print "- none"
  }
' /tmp/coverage.filtered.out | sort

echo
echo "Functions below 95%:"
go tool cover -func=/tmp/coverage.filtered.out | awk '$1 != "total:" && $3+0 < 95 && $2 !~ /buildRabbitMQ|rabbitMQTraceStrategy/ {print}'

echo
echo "Test status: PASS"
echo "Test skips/failures reported by go test -v:"
awk '/^--- SKIP:|^--- FAIL:|^FAIL[[:space:]]+github.com/ {print}' "${TEST_LOG}" | tail -n 80
if ! grep -Eq -- "^--- SKIP:|^--- FAIL:|^FAIL[[:space:]]+github.com" "${TEST_LOG}"; then
  echo "- none"
fi

echo
echo "Excluded from denominator:"
echo "- internal/builder.(*Builder).buildRabbitMQ"
echo "- internal/builder.(*Director).rabbitMQTraceStrategy"
echo "- RabbitMQ connection setup/cleanup branches in Builder.Close and Director.BuildBeelzebub"
echo "- Testcontainers tests are skipped when Docker is unavailable"
