# jj (Jujutsu) support for agents

Card `sweatshop-wu9`. King's request (2026-10-12): let agents use jj instead
of git. This is the design proposal the card asks for before any
implementation; the code is not changed yet.

## Where the line touches the agent's repository

Everything the line does with an agent's checkout goes through git, in two
places only:

- `station.Client.bundle` (`line/station/client.go`) resolves `rev` with
  `rev-parse`, pins it under a temporary `refs/sweatshop/bundle-*` ref, and
  runs `git bundle create ... ^refs/remotes/origin/master`.
- `maker.CheckTrailers` reads the `Bead:` / `Andon-Event:` trailers of `rev`
  with `git log --format=%(trailers)`.

Everything past that (witness, guard, line remote) receives a git bundle and
never sees the agent's VCS. So jj support is a question about how an agent's
jj repository answers those two calls, not about the line's server side.

## Decision 1: colocated, not native

Require a **colocated** repo (`jj git init --colocate`, or `jj git clone
--colocate`): `.git` sits beside `.jj`, and jj keeps it in sync after every
jj command. The line's unchanged git calls then work as they are.

Rejected: native jj (backing git store hidden at `.jj/repo/store/git`, no
`.git` in the workspace). Pointing `gitx.Repo` at the hidden store works only
by relying on jj's internal layout, which jj does not promise to keep, and
refs in it are only exported when jj decides to. Native support would need a
second bundle path (`jj git export` plus a layout probe) for no gain the
agent can see. Revisit only if jj makes colocation non-default and unsupported.

The station image `/work` is created by the line (a git clone of
`/line/remote.git`); colocating is one command in the station entrypoint:
`jj git init --colocate` inside the clone. `origin/master` appears to jj as
`master@origin`.

## Decision 2: the submitted commit is the jj change's current commit

jj has two identifiers: a change ID (stable across rewrites) and a commit ID
(the git SHA; changes on every rewrite). The line is git-addressed
throughout: attestations, the guard's compare-and-swap and the `RESULT:
merged <sha>` line all use the commit SHA. So:

- `sweatshop line merge --rev REV` takes whatever git can resolve. An agent
  passes the commit ID (`jj log -r @- --no-graph -T commit_id`) or a bookmark
  name. Change IDs are **not** accepted: they are not git objects, and
  resolving them means shelling out to jj, which couples the line to a tool
  the station may not have.
- The default `HEAD` is wrong under jj. In a colocated repo git `HEAD` is the
  parent of the working-copy commit `@`, which is the finished commit only if
  the agent ran `jj new` after describing it. The agent prompt for jj stations
  says: finish with `jj describe`, then `jj new`, then submit `HEAD` (= `@-`).
  No code change; `HEAD` already means that in a colocated repo.
- Because the SHA changes on each rewrite, the agent resubmits the *new*
  SHA after a rebase. This is the same as git rebase today; the guard keys on
  commit, never on change ID, so there is no mapping to store.

## Decision 3: rebase on a moved master

On `base_moved`, the jj equivalent of `git fetch origin && git rebase
origin/master` is:

    jj git fetch
    jj rebase -d master@origin

`jj rebase` rewrites the commit and, unlike git, never stops half-way: a
conflict is recorded *in* the commit. A conflicted commit exports to git
as a normal commit containing conflict-marker files, so it would reach the
witness and fail checks (or worse, pass if the markers are in an unchecked
file). The line must not rely on checks to catch this. Proposed guard in
`bundle`: refuse to submit a commit whose jj change is conflicted, detected
without jj by the commit's tree carrying jj's `.jjconflict-*` entries
(jj writes conflicted trees into git that way, per jj's git backend). A
test builds a tree with such an entry and expects `bundle` to fail. The
agent fixes it with `jj resolve` / editing, then resubmits.

The Maker prompt (`maker/prompt.go`, step 4) names the git rebase command.
A jj station gets a prompt variant with the commands above; the base prompt
stays git.

## What the line must not assume

1. **No git index or staging.** jj has no index; the working copy is a
   commit. Nothing in the line may call `git status`, `git add`, `git diff
   --cached`, or require a clean index. Today it does not (only plumbing:
   `rev-parse`, `update-ref`, `bundle`, `log`), and a test pins that `bundle`
   succeeds with an untouched `.git/index` absent.
2. **No checked-out branch.** A colocated jj repo has a detached `HEAD`
   and no current branch. The line must not read `HEAD`'s symbolic ref or
   require a branch name; it uses `refs/remotes/origin/master` and an explicit
   `rev` only.
3. **Working-copy commit is not a candidate by default.** `@` is mutable and
   snapshots on every jj command; the line submits only a resolved commit
   SHA, which is immutable once bundled.
4. **Branches are bookmarks.** jj bookmarks are not auto-advanced, and the line
   needs none: it never pushes (agents cannot reach the remote); the guard
   moves master.
5. **Temporary refs are visible to jj.** `bundle` creates and deletes
   `refs/sweatshop/bundle-*` in the shared git repo. jj imports git refs on its
   next command; because the ref is deleted before `bundle` returns, jj never
   sees it. This ordering is load-bearing and gets a test comment.
6. **Shallow clones.** jj does not support shallow git repos
   well (it can import but not fetch deepen). The line's shallow clone support
   (`TestMergeFromShallowCloneAfterRebase`) stays for git stations; jj
   stations get a full clone.

## Proposed implementation, if approved

Small and in this order, each its own commit:

1. `bundle`: reject conflicted trees (decision 3), with a test.
2. Station entrypoint option / engine flag `--vcs jj` that colocates `/work`
   and selects the jj prompt variant.
3. `maker/prompt.go`: jj variant of protocol steps 1 and 4.
4. A hermetic test running `jj` against a colocated clone, skipped when `jj`
   is not installed (like other external-tool tests), covering: submit `@-`,
   rebase onto a moved master, conflicted commit refused.

## Open questions for the King

- Is `jj` installed in the station image, or should the image build add it?
- Is requiring a full (non-shallow) clone for jj stations acceptable?
- Native jj repos: confirm we may leave them unsupported.
