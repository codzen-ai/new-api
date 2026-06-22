---
name: rebase-production
description: Rebase the local `production` branch onto the latest upstream `main`. Use when the user wants to sync production with upstream, pull in upstream changes, or asks to "rebase production onto main". Fetches upstream, fast-forwards main, rebases production, guides conflict resolution, and force-pushes to origin.
---

# Rebase production onto main

This repo carries the user's custom commits on the `production` branch, layered on top of
the upstream project. The job is to bring upstream's latest changes underneath those custom
commits by rebasing `production` onto `main`.

## Remote / branch layout (verify, don't assume)

- `origin`   → the user's fork; `production` tracks `origin/production`.
- `upstream` → the original project; `main` tracks `upstream/main`.

The flow is: refresh `main` from `upstream`, then rebase `production` on top of it, then
force-push `production` to `origin`.

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

2. **Fast-forward `main` to `upstream/main`.** Do this without checking out if possible, to
   avoid disturbing the working tree:
   ```
   git fetch upstream main:main
   ```
   If that fails because `main` isn't fast-forwardable, stop and report — `main` should never
   have diverged from upstream; investigate rather than force-update.

3. **Show what's about to happen.** Before rebasing, report the scope so the user knows the
   size of the job:
   ```
   git log --oneline main..production | wc -l      # commits being replayed
   git log --oneline production..main | wc -l       # new upstream commits coming in
   ```

4. **Rebase production onto main.**
   ```
   git switch production
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

Rebasing rewrites `production`'s history, so the push must be a force push. This is
outward-facing and rewrites shared history — **confirm with the user before pushing** unless
they've already told you to push in this session.

Always use the safe variant, never a bare `--force`:
```
git push --force-with-lease origin production
```
`--force-with-lease` aborts if `origin/production` moved since the last fetch (e.g. someone
else pushed), preventing you from clobbering their work.

## Wrap-up

Report: how many upstream commits were merged in, how many custom commits were replayed,
whether there were conflicts (and how they were resolved), and whether the push happened.
Leave the user back on the branch they started on if it wasn't `production` — otherwise stay on
`production`.

## Guardrails

- Never `git push --force` (bare) — only `--force-with-lease`.
- Never stash, reset, or discard uncommitted work without explicit user approval.
- Never force-update `main`; it must only ever fast-forward from `upstream/main`.
- If the working tree is dirty at the start, stop and surface it.
