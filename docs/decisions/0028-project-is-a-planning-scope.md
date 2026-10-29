# ADR 0028: A project is a planning scope; a task records its repository

**Status:** accepted. Supersedes the "every project is a Git repository"
clause of ADR 0017 and the cross-project edge rule of the dependency model.

**Context.** A project had two jobs at once: it was the board agents plan
against, and it owned a repository (root path and target branch) that
decided where a claim's worktree went. The two were welded together by
`atask init`: registering a repository was the only way to make work
claimable, and `init` also wrote `AGENTS.md`, `CLAUDE.md` and
`.claude/settings.json` into that repository's checkout.

That weld failed in practice. A single objective that spanned
`eapp-system`, `isa-platform` and `isa-dashboard` needed three
registrations, each of which would have edited a main checkout nobody
could commit to in that session, so the work was never filed. A registered
project that had since been bound to the wrong repository (`sah:everest-eapp`
pointed at `zyins`) could not be reused for the repository it was meant
for, and the docs had already drifted into contradiction: ADR 0017 says
every project is a repository, while `contract.md` says `project add`
creates one with no repository. Meanwhile the only thing that genuinely
needs a repository — binding evidence to a commit, and merging a
requirement's revision into a dependent's base — needs it per task, at
claim and verify time, not per planning scope.

**Decision.**

- A project is a planning scope: identity, name, plan revision, and the
  attempt budget. It names no directory, no repository, no branch and no
  workspace root.
- A task records `repo_root` (the canonical main-worktree root) and
  `base_ref` (the branch it starts from), resolved when it is filed from
  the working directory or `--repo`, defaulting the base ref to the
  repository's default branch. Both are fixed at creation. A task filed
  outside a Git repository records neither and runs its checks where it is
  claimed.
- `atask init` is optional. It creates the repository when needed and
  installs the agent instructions, and registers nothing. No command
  requires it, and no command infers which project is meant from the
  working directory: the scope is named with `--project` or is `general`.
- A dependency edge may join tasks in different projects and different
  repositories. A requirement in the same repository has its completed
  revision merged into the dependent's base; across repositories the edge
  only orders the work, because there is no common history to merge. A
  group's children share the group's repository so its proof can run on
  the merge of their revisions.
- Verification keeps binding evidence to `(repository, commit, check)`.
  That is the reason a repository appears anywhere in the model at all.

**Rationale.** The repository is an input to verification, not an identity.
Keeping it on the task makes filing independent of any setup step, lets one
planning scope span the repositories an outcome actually touches, and keeps
the two things that must be immutable — the home project after a claim, and
the base a worktree came from — both recorded where they are used.

**Consequences.** Projects lose `root_path`, `target_branch`,
`workspace_root` and the never-read `integration` column; `atask project
--target-branch` is gone, and a repository's base branch is its default
branch unless a task names another. `atask doctor` inspects a repository
plus the project's plan instead of a project. Registration as a concept is
gone, so nothing has to be announced before work can be filed. Migration
0026 backfills every existing task from its home project's repository;
migration 0027 rebuilds `projects` without the repository columns.
