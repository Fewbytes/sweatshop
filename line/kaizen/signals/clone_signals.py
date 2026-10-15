"""Clone signals that carry fault risk, from git history (kaizen.md section 3).

Counting clones is noise: many are deliberate (Kapser and Godfrey 2008). A pair
of files sharing code is reported only when its history says something:

- live: the files co-changed while staying similar (Mondal et al. 2014);
- unreconciled divergence: one copy changed and the pair grew less similar, and
  nobody has changed it back (Juergens et al. 2009: inconsistent clones);
- late propagation: it diverged and a later change to one copy alone restored
  the similarity, the riskiest pattern (Barbour et al. 2013).

Similarity is the number of CLONE_WINDOW_TOKENS-token windows two files share.
A divergence is a commit touching exactly one file that lowers it. Nothing here
unifies clones: a finding is routed to review or to a bug card.
"""

import hashlib
from collections import Counter, defaultdict
from dataclasses import dataclass
from itertools import combinations
from typing import Callable

from adapter import Commit, Signals
from file_signals import MIN_COCHANGES
from syntax import SourceFile, package_of

CLONE_WINDOW_TOKENS = 50
# Kim et al. 2005: 54-72% of the clone genealogies that vanished did so within
# about 8 check-ins, so a younger pair is not yet a refactor candidate.
MIN_CLONE_AGE_COMMITS = 8
TOP_CLONE_PAIRS = 50

WindowSet = frozenset[str]
# (commit sha, repo-relative path) -> window set of that file as of the commit,
# empty when the file did not exist.
WindowsAt = Callable[[str, str], WindowSet]


def window_hashes(tokens: list[str]) -> WindowSet:
    return frozenset(
        hashlib.sha1("\x00".join(tokens[i:i + CLONE_WINDOW_TOKENS]).encode()).hexdigest()
        for i in range(len(tokens) - CLONE_WINDOW_TOKENS + 1)
    )


def clone_fragments(files: dict[str, SourceFile]) -> Counter[str]:
    """Duplicated code per package: runs of consecutive windows that occur in
    2+ places (comments and whitespace are not tokens)."""
    places: dict[str, list[tuple[str, int]]] = defaultdict(list)
    for path, f in files.items():
        if f.is_test:
            continue
        for i in range(len(f.tokens) - CLONE_WINDOW_TOKENS + 1):
            text = "\x00".join(f.tokens[i:i + CLONE_WINDOW_TOKENS])
            places[hashlib.sha1(text.encode()).hexdigest()].append((path, i))
    duplicated: dict[str, set[int]] = defaultdict(set)
    for where in places.values():
        if len(where) > 1:
            for path, i in where:
                duplicated[path].add(i)
    fragments: Counter[str] = Counter()
    for path, starts in duplicated.items():
        for i in starts:
            if i - 1 not in starts:
                fragments[package_of(path)] += 1
    return fragments


@dataclass(frozen=True)
class PairRevision:
    """A commit that touched at least one file of the pair."""

    touched_a: bool
    touched_b: bool
    shared_after: int  # windows the two files share once the commit is applied
    commits_newer: int  # commits of the history made after this one


@dataclass(frozen=True)
class PairVerdict:
    support: int  # commits that changed both files and left them similar
    confidence: float  # support over the commits of the busier file (Zimmermann)
    divergence: str  # "none", "unreconciled" or "late_propagation"
    age_commits: int  # commits of the history since the pair first shared code
    refactor_candidate: bool


def judge_pair(shared_before: int, revisions: list[PairRevision], history_length: int) -> PairVerdict:
    """Read one pair's history, `revisions` oldest first. `shared_before` is the
    similarity before the first of them; when it is positive the pair is as old
    as the history, whose true start we cannot see."""
    support = 0
    changes_a = changes_b = 0
    previous = shared_before
    diverged = late = False
    born = history_length if shared_before else None
    for rev in revisions:
        changes_a += rev.touched_a
        changes_b += rev.touched_b
        support += rev.touched_a and rev.touched_b and rev.shared_after > 0
        alone = rev.touched_a != rev.touched_b
        if alone and rev.shared_after < previous:
            diverged = True
        elif diverged and rev.shared_after > previous:
            late = late or alone
            diverged = False
        if born is None and rev.shared_after > 0:
            born = rev.commits_newer
        previous = rev.shared_after
    age = born if born is not None else 0
    busiest = max(changes_a, changes_b)
    return PairVerdict(
        support=support,
        confidence=support / busiest if busiest else 0.0,
        divergence="late_propagation" if late else "unreconciled" if diverged else "none",
        age_commits=age,
        refactor_candidate=age >= MIN_CLONE_AGE_COMMITS,
    )


# What a finding is sent to. Deliberately no "unify": a clone that differs may
# differ on purpose.
ROUTES = {"late_propagation": "bug_card", "unreconciled": "review"}


def pair_record(a: str, b: str, shared: int, verdict: PairVerdict) -> Signals | None:
    """The reported form of a pair, None when its history is not worth a reader's time."""
    live = verdict.support >= MIN_COCHANGES
    if not live and verdict.divergence == "none":
        return None
    return {
        "a": a, "b": b, "shared_windows": shared,
        "live": live, "support": verdict.support, "confidence": round(verdict.confidence, 3),
        "divergence": verdict.divergence, "route": ROUTES.get(verdict.divergence),
        "age_commits": verdict.age_commits, "refactor_candidate": verdict.refactor_candidate,
    }


def rank_key(record: Signals) -> tuple:
    """Late propagation first, then unreconciled divergence, then live pairs by support and confidence."""
    severity = {"late_propagation": 0, "unreconciled": 1, "none": 2}[record["divergence"]]
    return severity, -record["support"], -record["confidence"], record["a"], record["b"]


def clone_pairs(files: dict[str, SourceFile], commits: list[Commit], windows_at: WindowsAt) -> list[Signals]:
    """File pairs that share code now, with what their history says. `commits`
    are newest first, as git log gives them. Ranked by rank_key."""
    owners: dict[str, list[str]] = defaultdict(list)
    for path, f in sorted(files.items()):
        if not f.is_test:
            for window in window_hashes(f.tokens):
                owners[window].append(path)
    shared_now: Counter[tuple[str, str]] = Counter()
    for paths in owners.values():
        shared_now.update(combinations(paths, 2))
    candidates = sorted(shared_now, key=lambda pair: (-shared_now[pair], pair))[:TOP_CLONE_PAIRS]

    found = []
    for a, b in candidates:
        touching = [(i, c) for i, c in enumerate(commits) if a in c.deleted_lines or b in c.deleted_lines]
        if not touching:
            continue
        oldest = touching[-1][1]
        revisions = [
            PairRevision(
                a in c.deleted_lines, b in c.deleted_lines,
                len(windows_at(c.sha, a) & windows_at(c.sha, b)), i,
            )
            for i, c in reversed(touching)
        ]
        before = len(windows_at(oldest.sha + "^", a) & windows_at(oldest.sha + "^", b))
        record = pair_record(a, b, shared_now[a, b], judge_pair(before, revisions, len(commits)))
        if record:
            found.append(record)
    return sorted(found, key=rank_key)
