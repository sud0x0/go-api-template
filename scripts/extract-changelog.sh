#!/usr/bin/env sh
# extract-changelog.sh prints the body of a CHANGELOG.md section.
#
# Usage:
#   extract-changelog.sh [--allow-empty] <version> [changelog-path]
#
# <version> is given WITHOUT the leading `v` (e.g. 1.2.3).
# changelog-path defaults to ./CHANGELOG.md.
#
# --allow-empty (must be the FIRST argument when present) skips ONLY the
# empty-body rejection. A missing section still exits 1 and usage errors still
# exit 2. CI uses the flag to assert the `## [Unreleased]` section EXISTS and
# parses. Right after a release cut that section is legitimately empty, while the
# release path (release.yml, `make changelog-check`) stays flagless so tagging
# enforces a non-empty section.
#
# Emptiness rule: a section body counts as EMPTY when, after IGNORING blank
# lines, `###` group headings (e.g. `### Added`), and HTML comments (both a
# full-line `<!-- ... -->` and a multi-line comment spanning the line with `<!--`
# to the line with `-->`), nothing remains. Everything else non-whitespace is
# real content. This governs only the empty-or-not DECISION. A non-empty body is
# still printed verbatim.
#
# Exit codes:
#   0  section found (body printed to stdout, and with --allow-empty an empty
#      body is OK)
#   1  section not found, or (without --allow-empty) the body is empty after
#      ignoring headings, comments, and blank lines
#   2  usage error, or changelog file missing
#
# The release workflow (.github/workflows/release.yml) pipes this into the
# GitHub Release body, so a missing or empty section ABORTS the release. The
# human-authored changelog is the single source of release notes. The script
# intentionally avoids GNU-only awk/sed flags so it works on macOS too.
# Index-based substring matching avoids regex-escaping concerns around dots in
# version strings.

set -eu

# --allow-empty must be the first argument when present.
ALLOW_EMPTY=0
if [ "${1:-}" = "--allow-empty" ]; then
    ALLOW_EMPTY=1
    shift
fi

if [ "$#" -lt 1 ]; then
    echo "usage: extract-changelog.sh [--allow-empty] <version> [changelog-path]" >&2
    exit 2
fi

VERSION="$1"
CHANGELOG="${2:-CHANGELOG.md}"

if [ ! -f "$CHANGELOG" ]; then
    echo "extract-changelog: $CHANGELOG not found" >&2
    exit 2
fi

# Build the exact heading we want to match. We match `## [VERSION]`. Trailing
# content (e.g. ` - 2026-01-01`) is fine but later digits are not, so `1.2.3`
# must not match `1.2.31`.
HEADING="## [$VERSION]"

# Step 1: confirm the heading exists, so an empty output cannot be ambiguous
# between "missing" and "empty body". Runs regardless of --allow-empty, so a
# missing section always exits 1.
if ! awk -v h="$HEADING" '
    substr($0, 1, length(h)) == h { found = 1; exit }
    END { exit !found }
' "$CHANGELOG"; then
    echo "extract-changelog: section '$HEADING' not found in $CHANGELOG" >&2
    exit 1
fi

# Step 2: capture every line between the matching heading and the next `## [`
# (or EOF). The heading line itself is not part of the body.
BODY=$(awk -v h="$HEADING" '
    BEGIN { in_section = 0 }
    {
        if (in_section && substr($0, 1, 4) == "## [") { in_section = 0 }
        if (in_section) { print; next }
        if (substr($0, 1, length(h)) == h) { in_section = 1 }
    }
' "$CHANGELOG")

# Step 3: trim leading and trailing blank lines without touching interior blank
# lines. This is what gets PRINTED for a non-empty section (verbatim).
TRIMMED=$(printf '%s\n' "$BODY" | awk '
    /[^[:space:]]/ { if (!first) first = NR; last = NR }
    { lines[NR] = $0 }
    END {
        if (!first) exit
        for (i = first; i <= last; i++) print lines[i]
    }
')

# Step 4: unless --allow-empty, reject an "empty" body. A heading with only the
# seeded group headings (or only an HTML comment) underneath is a
# release-process bug, where the operator forgot to write the entry before
# tagging. The emptiness test IGNORES blank lines, `###` group headings, and
# HTML comments. Anything else non-whitespace is real content. This affects only
# the decision. The printed body (Step 5) is unchanged.
if [ "$ALLOW_EMPTY" -eq 0 ]; then
    if ! printf '%s\n' "$BODY" | awk '
        BEGIN { incomment = 0; has = 0 }
        {
            line = $0
            # Inside a multi-line comment: swallow lines until the closing -->.
            if (incomment) {
                p = index(line, "-->")
                if (p > 0) {
                    incomment = 0
                    rest = substr(line, p + 3)
                    sub(/^[[:space:]]+/, "", rest)
                    if (rest ~ /[^[:space:]]/ && substr(rest, 1, 3) != "###") has = 1
                }
                next
            }
            t = line
            sub(/^[[:space:]]+/, "", t)
            if (t == "") next                    # blank line
            if (substr(t, 1, 3) == "###") next   # group heading
            o = index(line, "<!--")
            if (o > 0) {
                before = substr(line, 1, o - 1)
                sub(/^[[:space:]]+/, "", before)
                if (before ~ /[^[:space:]]/) has = 1   # real text before the comment
                afteropen = substr(line, o + 4)
                c = index(afteropen, "-->")
                if (c > 0) {
                    # Comment opens and closes on this line, so check any trailer.
                    rest = substr(afteropen, c + 3)
                    sub(/^[[:space:]]+/, "", rest)
                    if (rest ~ /[^[:space:]]/ && substr(rest, 1, 3) != "###") has = 1
                } else {
                    incomment = 1
                }
                next
            }
            has = 1   # ordinary non-blank, non-heading, non-comment line
        }
        END { exit (has ? 0 : 1) }
    '; then
        echo "extract-changelog: section '$HEADING' has no content in $CHANGELOG" >&2
        exit 1
    fi
fi

# Step 5: print the section body verbatim (leading and trailing blanks trimmed).
printf '%s\n' "$TRIMMED"
