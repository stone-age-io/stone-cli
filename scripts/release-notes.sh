#!/usr/bin/env bash
# Build the GitHub release body for a tag, and print it to stdout.
#
# Ported from the platform's script of the same name, for the same reason:
# goreleaser's own changelog is a list of commit subjects, and this repo's
# reasoning lives in CHANGELOG.md as paragraphs. The part an operator actually
# needs -- that `stone leaf-node` is gone because the collection is, that a flag
# which used to report success was writing nothing -- exists nowhere in the
# commit log. So the release body is the changelog section for the tag, wrapped
# in the install prose that used to sit in .goreleaser.yaml as `release.header`
# and `release.footer`.
#
# Those two keys are gone from .goreleaser.yaml deliberately: goreleaser does not
# apply them to a --release-notes body, so leaving them there would be prose that
# renders on a manual run and vanishes on a real one. Assembling the whole body
# here means there is one definition of it, and it can be read and diffed before
# the tag is pushed.
#
# Usage:  scripts/release-notes.sh v0.3.0
#
# A FINAL tag requires its own `## [X.Y.Z]` section and fails without one, so a
# release cannot ship with an empty body because someone forgot to promote
# `## [Unreleased]`. A PRERELEASE tag (v0.3.1-rc1) reads `## [Unreleased]`
# instead, because that is where the content for an unreleased version is by
# definition -- an rc has no section of its own and never will.
set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: $0 <tag>   e.g. $0 v0.3.0" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANGELOG="$ROOT/CHANGELOG.md"
SCHEMA_SOURCE="$ROOT/cmd/testdata/schema-source.txt"

VERSION="${TAG#v}"
case "$TAG" in
  *-*) HEADING='## [Unreleased]' ; KIND='prerelease' ;;
  *)   HEADING="## [$VERSION]"   ; KIND='release' ;;
esac

# Everything between this heading and the next `## ` heading, with surrounding
# blank lines trimmed. awk rather than sed: the section runs to a hundred lines
# and contains `#` inside fenced code blocks, so the boundary has to be anchored
# to the start of a line and to the two-hash level exactly.
SECTION="$(
  awk -v want="$HEADING" '
    index($0, want) == 1 && substr($0, length(want) + 1, 1) ~ /^[ ]?$/ { grab = 1; next }
    grab && /^## / { exit }
    grab { print }
  ' "$CHANGELOG"
)"

# Trim leading and trailing blank lines.
SECTION="$(printf '%s\n' "$SECTION" | sed -e '/./,$!d' -e ':a' -e '/^\n*$/{$d;N;ba' -e '}')"

if [ -z "$(printf '%s' "$SECTION" | tr -d '[:space:]')" ]; then
  echo "release-notes: no content under '$HEADING' in CHANGELOG.md" >&2
  echo "release-notes: a $KIND tag needs that section filled in before tagging." >&2
  exit 1
fi

# The platform release this CLI's field table was last checked against. Read
# from the vendored schema's provenance file rather than typed here, because
# there is already exactly one place that records it and two would disagree --
# which is the entire failure mode this repo's drift guard exists to catch.
#
# Required, not optional: a release that cannot say which platform it was built
# against is a release whose compatibility nobody can check, and the answer is
# one line in a file the schema refresh already touches.
PLATFORM_RELEASE="$(sed -n 's/^platform release:[[:space:]]*//p' "$SCHEMA_SOURCE" | head -1)"
if [ -z "$PLATFORM_RELEASE" ]; then
  echo "release-notes: no 'platform release:' line in cmd/testdata/schema-source.txt" >&2
  echo "release-notes: add one naming the platform release this schema came from." >&2
  exit 1
fi

cat <<EOF
## stone $TAG

The command-line client for the Stone-Age.io platform. Extract the binary onto a
laptop or CI runner and point it at a running server:

\`\`\`
stone context create local --url https://platform.example.com
stone auth login
\`\`\`

**\`stone\` is not \`stone-age\`.** This is the client; \`stone-age\` is the
control-plane server, released from the
[platform](https://github.com/stone-age-io/platform) repo. They share a project,
not a job.

---

$SECTION

---

### Compatibility

Built and tested against platform **$PLATFORM_RELEASE**. The two are versioned
independently — a number here does not have to match a number there.

The CLI's field list is hand-maintained rather than derived from the platform's
\`schema.json\`, so a given \`stone\` can lag a platform release. If a field
exists in the console but has no flag, that is why. A vendored copy of the
schema is checked against the field table on every build, so a collection that
has moved fails loudly rather than reporting success and writing nothing.

Pre-1.0: treat a minor version as potentially breaking, and pin what you deploy.

**Full changelog**: https://github.com/stone-age-io/stone-cli/commits/$TAG
EOF
