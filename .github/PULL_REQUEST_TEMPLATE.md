## What this changes

<!-- Description and motivation. Link the related issue: Fixes #NN -->

## Checklist

- [ ] Tests cover: fires correctly, does not fire on clean input, and
      degradation behavior (conservative detection: prefer a false
      negative over a fabricated attribute)
- [ ] `make test` passes locally
- [ ] New attributes carry evidence locators and an honest confidence tier
      (declared / inferred / unresolved)
- [ ] No new required permissions without a matching RBAC and docs update
- [ ] Docs updated if behavior or configuration changed

## Notes for reviewers

<!-- Anything needing extra scrutiny. Example AIBOM CR output is welcome. -->

Contributions require the [Google CLA](https://cla.developers.google.com/).
