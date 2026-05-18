# [MEDIUM] CHANGELOG.md content interpolated into awk regex via shell-rendered VERSION

**File:** [`.github/workflows/release.yaml`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/.github/workflows/release.yaml#L213-L214) (lines 213, 214)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** medium  •  **Slug:** `other-changelog-awk-injection`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

L213-214 strips the leading `v` from the tag and embeds the result into an awk regex: `awk "/^## \\[v?${VERSION}\\]/{found=1; next} ..."`. VERSION ultimately comes from `github.ref_name`. A tag containing awk regex metacharacters (`.`, `[`, `]`, `*`, `\`, etc.) could broaden or break the match, potentially extracting CHANGELOG content the maintainer did not intend to publish or matching nothing and silently falling back to git log. Lower severity than the broader injection finding because awk's `"…"` quoting and refname constraints limit shell-level escape, but it compounds with the github.ref_name script-injection class above. Worth fixing as part of the same hardening.

## Recommendation

Pass VERSION to awk via `-v ver="$VERSION"` and reference `ver` inside the program; or use a shell-escaped literal-match approach (grep -F + line range). Avoid embedding shell-derived strings into awk regex source text.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
