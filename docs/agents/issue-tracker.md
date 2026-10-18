# Issue tracker: GitHub

Specs and tickets for this repo live as GitHub issues in `codzen-ai/new-api`. Use the `gh` CLI for all operations.

**Always pass `--repo codzen-ai/new-api`** (`gh api repos/codzen-ai/new-api/...` for API calls). This repo is a fork of `QuantumNous/new-api`, and `gh` must never create or edit anything upstream. The Linear server in `.mcp.json` (`linear-codzen`) is not the tracker for specs and tickets.

**Specs and tickets are both issues, told apart by structure:**

- A **spec** is an issue labelled `spec`, with the spec text as its body. Its body ends with a pointer to the repo docs (`AGENTS.md`, and `.agents/rules/` where relevant).
- A **ticket** is a **sub-issue** of its spec. Its task graph is expressed through GitHub's native **issue dependencies** (`blocked by`), never through nesting tickets under each other.
- Issues not tied to a spec (bugs, triage) are plain issues with no parent and no `spec` label.

## Spec size

A spec lands as one pull request, so its size is the size of the review. Keep each spec to:

- a PR a human can review in 30–60 minutes;
- 2–4 agent tickets (`ready-for-agent`); `ready-for-human` tickets don't count;
- one outcome that can be demonstrated on its own once merged.

When a plan is bigger, split it into several specs in dependency order (each its own `spec` issue), rather than one large spec. Generated files, lockfiles, i18n locale syncs and pure reformat commits don't count toward the review size.

## Specs

- **Publish a spec** (`/to-spec`): `gh issue create --repo codzen-ai/new-api --label spec --title "<spec name>" --body-file <file>`. Triage labels don't apply to specs.
- **Fetch a spec**: `gh issue view <n> --repo codzen-ai/new-api --json number,title,body,labels,comments`; list its tickets with `gh api repos/codzen-ai/new-api/issues/<n>/sub_issues --jq '.[] | {number, title, state, labels: [.labels[].name]}'`.
- **Update a spec**: `gh issue edit <n> --repo codzen-ai/new-api --body-file <file>` with the whole new body; say what changed in a comment (`gh issue comment`), so the history is readable.
- **Spec work started / finished**: assign the spec (`--add-assignee @me`) when the first ticket is claimed. It closes with its pull request (below); if every ticket is closed some other way, close it with `gh issue close <n> --repo codzen-ai/new-api --comment "..."`. "Close the spec" means this.
- **Comment on a spec**: `gh issue comment <n> --repo codzen-ai/new-api --body "..."`.

## Tickets

- **Create a ticket** (`/to-tickets`): `gh issue create --repo codzen-ai/new-api --parent <spec> --label ready-for-agent --title "..." --body-file <file>` (`ready-for-human` for a person's ticket). Start the body with a `## Spec` section linking the spec (`#<spec>`).
- **Add a blocker**: `gh api --method POST repos/codzen-ai/new-api/issues/<ticket>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, where `<blocker-db-id>` is the blocker's numeric database id (`gh api repos/codzen-ai/new-api/issues/<n> --jq .id`), not its `#number` or `node_id`.
- **Is it blocked?** List its open blockers: `gh api repos/codzen-ai/new-api/issues/<n>/dependencies/blocked_by --jq '[.[] | select(.state == "open") | .number]'`. Do not rely on `issue_dependencies_summary`: it is updated with a delay. A ticket is unblocked when every blocker is closed.
- **Read an issue**: `gh issue view <n> --repo codzen-ai/new-api --json number,title,body,labels,assignees,comments`.
- **List issues**: `gh issue list --repo codzen-ai/new-api --state open --json number,title,labels,assignees` with `--label` filters.
- **Claim**: `gh issue edit <n> --repo codzen-ai/new-api --add-assignee @me`.
- **Apply / remove labels**: `gh issue edit <n> --repo codzen-ai/new-api --add-label "..."` / `--remove-label "..."`.
- **Close**: `gh issue close <n> --repo codzen-ai/new-api --comment "<outcome>"`. For won't-do, `--reason "not planned"` and the `wontfix` label.

Refer to issues as `#<n>`. GitHub shares one number space across issues and pull requests, so a bare `#42` may be either: resolve with `gh issue view 42` and fall back to `gh pr view 42`.

## Status

| State | Meaning |
| --- | --- |
| open, no assignee | Not started; `ready-for-agent` / `ready-for-human` say who can take it |
| open, assigned | Claimed; whoever is assigned is working on it |
| closed (completed) | Done |
| closed (not planned) | Will not be actioned |

## Closing work: through pull requests

**This tracker closes work through PRs: yes.** Every spec lands as **one pull request** from its integration branch into `staging`, the default branch (`/implement-spec` opens it as a draft after the first merge and marks it ready at the end). Ticket branches are internal and never get their own PR. `production` is promoted from `staging` by a person, never by an agent.

- Open the PR in `codzen-ai/new-api` (`gh pr create --repo codzen-ai/new-api --base staging`), never upstream.
- PR title: the spec's title. PR body: `Closes #<spec>` and a `Closes #<n>` line for every agent ticket, so merging closes them. Leave `ready-for-human` tickets open: a person closes them when their step is done.
- A human decides the merge. Agents never merge to `staging` or `production`.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill says "publish to the issue tracker"

- Publishing a **spec** → an issue labelled `spec` (see Specs).
- Publishing **tickets** → sub-issues of that spec, with their blockers (see Tickets).
- Anything else (a bug, a triage item) → a plain issue.

## When a skill says "fetch the relevant ticket"

Read it as in **Read an issue**. If the skill means the spec (e.g. `/implement-spec <n>`), fetch the spec and its sub-issues instead.

## When a skill says "the spec and its tickets"

The spec issue's body, plus every sub-issue of it. Skip sub-issues labelled `ready-for-human`: they are for a person, not an agent.
