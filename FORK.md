# collectives-friends/multica — True Collective fork

This repository is True Collective's fork of the public
[multica-ai/multica](https://github.com/multica-ai/multica) project. It carries
the changes the TC installation needs on top of an upstream release; everything
else follows upstream, and the Multica identity (LICENSE Part I + Apache-2.0,
name, logos) is kept unchanged.

## How it is consumed

`collectives-friends/tc-multica` pins this fork by commit in `source.lock.json`
and mirrors the same commit in `source-ci.lock.json`. Backend, branded web,
Claude runtime and Codex runtime all build from that exact archive
(`codeload.github.com/collectives-friends/multica/tar.gz/<commit>` with a
SHA256 checksum), and the attested source CI runs the pinned source's own
checks inside `tc-multica`.

Bumping the pin is a reviewed PR in `tc-multica`, not an automatic follow:

1. merge the sync PR here (`main`),
2. read any new upstream migrations (they run at backend startup),
3. update `source.lock.json` + `source-ci.lock.json` in `tc-multica`,
   re-validate the overlay guards and run the attested source CI on the new
   commit before the release PR.

## True Collective changes on top of upstream

Regenerate this overview at any time:

```sh
git fetch upstream
git log --oneline upstream/main..main
git diff --stat upstream/main..main
```

Current delta (upstream v0.6.1 → fork `main`, 13 commits):

- workspace feature flags declared through deployment configuration
  (`c61ee3b6e`, `40f2ea27c`, `35dcbfd1f`, `df7f61032`, `55ae406b8`)
- gated project integrations and host callback authority (TCO-272, `90498843f`)
- project-level custom properties and the plugin property target
  (TCO-293, `9b631e8ff`)
- workspace connection registry and host-proxied plugin credentials
  (`a03163b2c`, `9dbeead7d`, `8b42309d1`), published as the `connections_v1`
  host flag (`faf2f8f90`)
- plugin hooks use their consented timeout (`5c9388925`)
- `multica-dev` system-map skill for agents (`b29572b4e`)

## Upstream sync

`.github/workflows/upstream-sync.yml` runs weekly (Mondays 05:00 UTC) and on
manual dispatch. It merges `multica-ai/multica@main` into a
`sync/upstream-<upstream-sha>` branch and opens a reviewable PR against `main`.
It never pushes to `main`, never auto-merges, and aborts (with a visible
failure) if the merge conflicts — a conflicting sync needs a manual branch.

Prerequisite: none — GitHub runs this repository's CI on pull requests
(verified 2026-10-13 with `#16`: `changes`, `backend` and `frontend` passed and
CodeQL ran).

Notes:

- The PR is opened by `github-actions[bot]`. GitHub suppresses `pull_request`
  workflow runs for PRs created with the repository `GITHUB_TOKEN`, so the
  sync workflow dispatches the fork's `CI` workflow (`workflow_dispatch`) on
  the sync branch immediately after opening the PR. Review the CI run from
  the Actions tab for that branch; it is not attached to the PR as a check.
  The hosted platform suites are covered by the `collectives-friends/multica-ci`
  mirror that `tc-multica`'s attested source CI verifies.
- Merge only with green CI and a read-through of new upstream migrations.
- Upstream release tags (`vX.Y.Z`) are the upstream release points. Jumping
  version lines is a deliberate decision with migration review, not part of
  the weekly merge.

Manual fallback (use when the automation is unavailable):

```sh
git fetch upstream main
git switch -c sync/upstream-manual main
git merge --no-edit upstream/main   # resolve conflicts here, if any
git push origin sync/upstream-manual
gh pr create --base main --head sync/upstream-manual
```

## Deployment pin hygiene

`tc-multica` pins this repository by commit. Prefer a commit on `main` when
bumping (`git branch -r --contains <sha>` should show `origin/main`), so the
deployed revision is traceable to the fork's main line.

