---
name: rebase-staging
description: Rebase the local `staging` branch onto the latest upstream `main`. Use when the user wants to sync the fork with upstream, pull in upstream changes, or asks to "rebase staging onto main" / "rebase production" (production is only a verified pointer — the rebase always targets staging). Fetches upstream, fast-forwards main, rebases staging, guides conflict resolution, and force-pushes to origin.
---

# Rebase staging onto main

This fork keeps **one** custom history, on `staging`. `production` is not an independent
branch to rebase — it is a pointer to a `staging` commit that has been verified in the
staging environment (see `specifications/git-workflow.md`). So "sync with upstream" always
means: rebase `staging` onto `main`.

If the user asks to "rebase production", say what this skill does instead and proceed on
`staging` — do not rebase `production` as a separate line of history.

## Remote / branch layout (verify, don't assume)

- `origin`   → the user's fork; `staging` tracks `origin/staging`.
- `upstream` → the original project; `main` tracks `upstream/main` (read-only mirror).
- `production` → pointer only, moved with `git branch -f production <verified-sha>` after
  staging verification. Never rebased, never committed to.

The flow is: refresh `main` from `upstream`, rebase `staging` on top of it, force-push
`staging` to `origin`.

## Preconditions — check before doing anything

1. **Clean working tree.** Run `git status --porcelain`. If it's non-empty, STOP and tell the
   user — do not stash or discard their work without asking.
2. **Know the starting branch** so you can report it. `git rev-parse --abbrev-ref HEAD`.
3. Confirm the remotes exist as expected: `git remote -v`. If `upstream` is missing, stop and
   ask the user for the upstream URL instead of guessing.

## Steps

1. **Fetch upstream and origin.**
   ```
   git fetch upstream --prune
   git fetch origin --prune
   ```

2. **Fast-forward `main` to `upstream/main`.** Do this without checking out, to avoid
   disturbing the working tree:
   ```
   git fetch upstream main:main
   ```
   If that fails because `main` isn't fast-forwardable, stop and report — `main` is a pure
   mirror and should never have diverged; investigate rather than force-update.

3. **Show what's about to happen.** Before rebasing, report the scope:
   ```
   git log --oneline main..staging | wc -l      # commits being replayed
   git log --oneline staging..main | wc -l      # new upstream commits coming in
   ```

4. **Rebase staging onto main.**
   ```
   git switch staging
   git rebase main
   ```

5. **Handle conflicts.** If the rebase stops on a conflict:
   - Run `git status` to see the conflicted files.
   - Resolve each conflict. Favor preserving the user's custom intent while accepting upstream's
     structural changes; when a resolution is ambiguous, ask the user rather than guessing.
   - After resolving: `git add <files>` then `git rebase --continue`.
   - Repeat until the rebase completes.
   - If things go wrong and you need a clean slate, `git rebase --abort` returns to the
     pre-rebase state. Offer this before doing anything destructive.

6. **Sanity check after the rebase completes.**
   - `git log --oneline -10` to confirm the history looks right (custom commits on top of the
     new upstream HEAD).
   - If the project builds quickly, consider a build/test check — but only if the user asked or
     it's cheap. Don't block on it otherwise.

## Force-pushing to origin

Rebasing rewrites `staging`'s history, so the push must be a force push. This is
outward-facing and rewrites shared history — **confirm with the user before pushing** unless
they've already told you to push in this session. Pushing `staging` also triggers the
staging image build/deploy, so it is a deployment, not just a push.

Always use the safe variant, never a bare `--force`:
```
git push --force-with-lease origin staging
```
`--force-with-lease` aborts if `origin/staging` moved since the last fetch (e.g. someone
else pushed), preventing you from clobbering their work.

## After verification: moving the production pointer

This is a **separate, explicitly requested step** — never do it as part of a rebase. Only
after the user confirms the rebased `staging` is verified in the staging environment:

```
git branch -f production <verified-staging-sha>
git tag prod-YYYYMMDD <verified-staging-sha>
git push --force-with-lease origin production
git push origin prod-YYYYMMDD
```

Rollback is moving `production` back to the previous `prod-*` tag. If `production` is not
currently an ancestor of `staging` (`git merge-base --is-ancestor production staging`), the
histories have diverged — stop and surface that instead of force-moving the pointer.

## Wrap-up

Report: how many upstream commits were merged in, how many custom commits were replayed,
whether there were conflicts (and how they were resolved), and whether the push happened.
Leave the user back on the branch they started on if it wasn't `staging` — otherwise stay on
`staging`.

## Guardrails

- Never `git push --force` (bare) — only `--force-with-lease`.
- Never stash, reset, or discard uncommitted work without explicit user approval.
- Never force-update `main`; it must only ever fast-forward from `upstream/main`.
- Never rebase or commit on `production`; it is a pointer, moved only on explicit request.
- If the working tree is dirty at the start, stop and surface it.
