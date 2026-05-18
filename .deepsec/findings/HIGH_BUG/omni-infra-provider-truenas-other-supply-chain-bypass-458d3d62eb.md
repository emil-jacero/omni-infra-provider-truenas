# [HIGH_BUG] POISONED marker write error ignored — compromised ISO can be reused on next provision

**File:** [`internal/provisioner/steps.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/provisioner/steps.go#L334) (lines 334)
**Project:** omni-infra-provider-truenas
**Severity:** HIGH_BUG  •  **Confidence:** high  •  **Slug:** `other-supply-chain-bypass`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

When the TOFU hash check detects a mismatch, the code marks the stored hash with a POISONED prefix so future cache hits refuse to reuse the file: `_ = p.client.SetDatasetUserProperty(ctx, isoDataset, isoHashProperty, poisonMarker(downloadedHash))`. The error is dropped. If this write fails (transient WebSocket error, permission issue, race with another provision), the on-disk ISO bytes — which by this point are confirmed to mismatch the prior trusted hash, i.e., are believed compromised — remain on the dataset with a non-poisoned property value (either the prior trusted hash or empty). The next provision goes through `FileExists → cache hit → cachedISOPoisoned("")=false` and uses the compromised file to boot Talos VMs. The current provision still fails loudly (good), but the dangerous bytes are not reliably quarantined, which is the entire point of the marker.

## Recommendation

Treat the marker write as a critical step: retry on transient failure, and if the marker cannot be persisted, delete the compromised file from disk (or at minimum surface the failure prominently in the operator-facing error so the manual cleanup recipe in docs/hardening.md is triggered). Returning the original mismatch error while leaving the bytes accessible silently weakens the protection.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
