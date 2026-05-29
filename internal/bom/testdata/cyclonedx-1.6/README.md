# Vendored CycloneDX 1.6 JSON schemas

These schemas are used by the BOM builder's test suite to validate every
generated BOM against the official CycloneDX 1.6 specification. They are
NOT compiled into the production controller binary — validation runs in
tests only.

## Pin

- **Upstream:** https://github.com/CycloneDX/specification
- **Tag:** `1.6.1`
- **Fetched:** 2026-05-10

## Files

| File | sha256 | Purpose |
|------|--------|---------|
| `bom-1.6.schema.json` | `efc54d749e32a6e16abd19394b80b4c67d846e12c782e04505130375f94ea541` | Main BOM schema (draft-07) |
| `spdx.schema.json` | `c41917196639055e9f9670811bac23ef777732144f3ff5a2f39686f61580dbe6` | SPDX license enum referenced by the BOM schema |
| `jsf-0.82.schema.json` | `8bae002c25e723db7ee1f26afde680ae1a2b1a8f6b4b4b0fd65dc3becb090aae` | JSON Signature Format referenced for BOM signatures |

Recompute with `shasum -a 256 *.json` after refreshing.

## When to refresh

- A new patch release in the CycloneDX 1.6 line (1.6.2, 1.6.3, ...). Bump the
  tag, re-fetch, regenerate the sha256s, run the test suite.
- A new minor or major (1.7, 2.0). That's a deliberate spec upgrade — open
  a separate change that bumps both the schema files AND the BOM builder
  output to match the new version.

Do NOT refresh from `master`/`main`. Pin to a release tag so the build is
reproducible.
