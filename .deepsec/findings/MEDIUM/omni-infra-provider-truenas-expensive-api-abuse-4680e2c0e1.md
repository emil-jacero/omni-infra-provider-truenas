# [MEDIUM] ISO cache-hit branch trusts on-disk bytes and never re-hashes

**File:** [`internal/provisioner/steps.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/provisioner/steps.go#L222-L241) (lines 222, 223, 229, 236, 237, 240, 241)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** medium  •  **Slug:** `expensive-api-abuse`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

On `FileExists == true`, the code only inspects the stored property for a `POISONED-` prefix and proceeds to use the on-disk file without re-hashing it. An attacker with write access to the ISO dataset (a non-Omni TrueNAS admin, a misconfigured share, a separate workload sharing the pool) can swap the ISO bytes after the initial TOFU baseline is recorded; the swap is invisible because no future provision recomputes the hash. Combined with the prior two findings (property-read errors silently treated as 'no baseline' and POISONED-marker write failures), the at-rest tamper window is effectively unbounded. The `tofu_pinned: true` log field falsely advertises that the bytes are verified.

## Recommendation

On cache hit, re-hash the on-disk file (or at least sample-hash + size check) and compare against the stored TOFU hash before reusing. If the cost is too high to do every provision, gate it behind a configurable freshness window or do it lazily on the first cache hit after provider restart.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
