# [MEDIUM] ZFS encryption passphrase stored as plaintext user property on the encrypted dataset itself

**File:** [`internal/provisioner/steps.go`](https://github.com/bearbinary/omni-infra-provider-truenas/blob/main/internal/provisioner/steps.go#L71-L1425) (lines 71, 74, 1397, 1399, 1406, 1413, 1418, 1425)
**Project:** omni-infra-provider-truenas
**Severity:** MEDIUM  •  **Confidence:** high  •  **Slug:** `secrets-exposure`

## Owners

**Suggested assignee:** `43915749+Cliftonz@users.noreply.github.com` _(via last-committer)_

## Finding

`generatePassphrase()` creates a 32-byte random passphrase, then `ensureZvol` writes it to the encrypted zvol via `client.UserProperty{Key: passphraseProperty, Value: passphrase}` where `passphraseProperty = "org.omni:passphrase"`. ZFS user properties are metadata that can be read with `zfs get` WITHOUT the encryption key — they live alongside the dataset, not inside it. This collapses the at-rest threat model: anyone who has the storage (stolen disks, exported pool, snapshot replication, lower-privileged TrueNAS read access that can call `pool.dataset.query`/`zfs.dataset.user_props_query`) can read the passphrase and unlock the data. Encryption-at-rest provides essentially no protection against any actor who can also read dataset metadata. The same passphrase is also re-read during the `isAlreadyExists` recovery path (`p.client.GetDatasetUserProperty(ctx, zvolPath, passphraseProperty)`), confirming the design expects long-term plaintext storage on the device being encrypted. If the design is intentional (provider needs unattended unlock), the docs should explicitly call this out so operators don't believe the `encrypted: true` flag protects them from a stolen-pool scenario.

## Recommendation

Either (a) document in `docs/hardening.md` that `encrypted: true` only protects against threat models where the attacker has the disks but no metadata access, and is NOT a defense against any TrueNAS user with property-read permissions; or (b) move the passphrase out of the dataset itself — store it in a TrueNAS system keychain, an external KMS, or a separate non-replicated/restricted dataset, so the key does not travel with the encrypted bytes.

## Recent committers (`git log`)

- Zac Clifton <43915749+Cliftonz@users.noreply.github.com> (2026-04-27)
