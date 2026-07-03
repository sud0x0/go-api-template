#!/usr/bin/env sh
# extract-changelog.sh — print the body of a CHANGELOG.md section.
#
# Usage:
#   extract-changelog.sh <version> [changelog-path]
#
# <version> is given WITHOUT the leading `v` (e.g. 1.2.3).
# changelog-path defaults to ./CHANGELOG.md.
#
# Exit codes:
#   0  section found, body printed to stdout
#   1  section not found, or section body is empty/whitespace-only
#   2  usage error, or changelog file missing
#
# The script intentionally avoids GNU-only awk/sed flags so it works on
# macOS too. Index-based substring matching avoids regex-escaping
# concerns around dots in version strings.

set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: extract-changelog.sh <version> [changelog-path]" >&2
    exit 2
fi

VERSION="$1"
CHANGELOG="${2:-CHANGELOG.md}"

if [ ! -f "$CHANGELOG" ]; then
    echo "extract-changelog: $CHANGELOG not found" >&2
    exit 2
fi

# Build the exact heading we want to match. We match `## [VERSION]` —
# trailing content (e.g. ` - 2026-01-01`) is fine but later digits are
# not (`1.2.3` must not match `1.2.31`).
HEADING="## [$VERSION]"

# Step 1: confirm the heading exists. Without this check, an empty
# output could mean either "missing" or "empty body" — we want to
# distinguish them in the error message.
if ! awk -v h="$HEADING" '
    substr($0, 1, length(h)) == h { found = 1; exit }
    END { exit !found }
' "$CHANGELOG"; then
    echo "extract-changelog: section '$HEADING' not found in $CHANGELOG" >&2
    exit 1
fi

# Step 2: extract the body. Print every line between the matching
# heading and the next `## [` (or EOF). The heading line itself is not
# part of the body.
BODY=$(awk -v h="$HEADING" '
    BEGIN { in_section = 0 }
    {
        if (in_section && substr($0, 1, 4) == "## [") { in_section = 0 }
        if (in_section) { print; next }
        if (substr($0, 1, length(h)) == h) { in_section = 1 }
    }
' "$CHANGELOG")

# Step 3: trim leading and trailing blank lines without touching
# interior blank lines.
TRIMMED=$(printf '%s\n' "$BODY" | awk '
    /[^[:space:]]/ { if (!first) first = NR; last = NR }
    { lines[NR] = $0 }
    END {
        if (!first) exit
        for (i = first; i <= last; i++) print lines[i]
    }
')

# Step 4: reject empty bodies. A heading with no notes underneath is a
# release-process bug — the operator forgot to write the entry before
# tagging.
if [ -z "$TRIMMED" ]; then
    echo "extract-changelog: section '$HEADING' has no content in $CHANGELOG" >&2
    exit 1
fi

printf '%s\n' "$TRIMMED"
