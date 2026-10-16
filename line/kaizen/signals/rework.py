"""Survival and rework of landed cards' code, read from git (kaizen.md, section 3).

A card's commits are the first-parent commits whose message carries its
`Bead: <id>` trailer. All numbers are descriptive counts with their sample
sizes: no benchmark exists, so nothing here is thresholded or gated.

  survival  share of the card's added lines still attributed to the card's
            commits by `git blame` at the last master commit at or before
            landing + 7 (30) days. Lines in a file that was renamed or deleted
            before that snapshot count as gone. A card younger than the horizon
            at HEAD is left out of that horizon's sample, never counted as 0.
  rework    lines of the card's commits that a later card's commit changed or
            removed within 14 days of landing, and how many of those by a card
            of the same family (same parent card).
  reverts   commits whose message says `This reverts commit <sha>` of one of
            the card's commits; follow-up bug cards are bug cards that name the
            card id.
"""

import bisect
import re
import subprocess
from collections import Counter, defaultdict
from dataclasses import dataclass, field

DAY_SECONDS = 86400
SURVIVAL_DAYS = (7, 30)
REWORK_DAYS = 14

BEAD_TRAILER = re.compile(r"^Bead:[ \t]*(\S+)[ \t]*$", re.MULTILINE)
REVERTS_LINE = re.compile(r"This reverts commit ([0-9a-f]{40})")
BLAME_HEADER = re.compile(r"^([0-9a-f]{40}) \d+ \d+")
HUNK_HEADER = re.compile(r"^@@ -(\d+)(?:,(\d+))? \+\d+(?:,\d+)? @@")
GROUPINGS = ("maker", "card_type", "area")


@dataclass(frozen=True)
class HistoryCommit:
    sha: str
    committed_at: int
    author: str
    body: str
    parents: tuple[str, ...]
    added: dict[str, int]  # path -> lines added


@dataclass
class FileRecord:
    """What happened to the lines one card added to one file."""

    card: str
    maker: str
    card_type: str
    area: str
    added: int
    survived: dict[int, int | None] = field(default_factory=dict)  # horizon days -> lines, None when too young
    reworked: int | None = 0  # None when the card is younger than REWORK_DAYS at HEAD
    reworked_same_family: int | None = 0


def run_git(root: str, *args: str) -> str:
    return subprocess.run(["git", "-C", root, *args], check=True, capture_output=True, text=True).stdout


def read_history(root: str, head: str) -> list[HistoryCommit]:
    """First-parent commits of head, oldest first."""
    out = run_git(root, "log", "--first-parent", "--reverse", "--no-renames", "--numstat",
                  "--format=%x1e%H%x00%ct%x00%an%x00%P%x00%B%x00", head)
    commits = []
    for record in out.split("\x1e")[1:]:
        sha, ct, author, parents, body, numstat = record.split("\x00", 5)
        added = {}
        for line in numstat.splitlines():
            if not line:
                continue
            adds, _, path = line.split("\t", 2)
            if adds != "-":  # binary
                added[path] = int(adds)
        commits.append(HistoryCommit(sha, int(ct), author, body, tuple(parents.split()), added))
    return commits


def card_of(commit: HistoryCommit) -> str | None:
    found = BEAD_TRAILER.findall(commit.body)
    return found[-1] if found else None


def blame_origins(root: str, rev: str, path: str, ranges: list[tuple[int, int]] | None = None) -> Counter[str]:
    """Lines of path at rev by the commit git blame attributes them to; empty when path is absent at rev."""
    if subprocess.run(["git", "-C", root, "cat-file", "-e", f"{rev}:{path}"], capture_output=True).returncode != 0:
        return Counter()
    line_ranges = [f"-L{start},{end}" for start, end in ranges or []]
    out = run_git(root, "blame", "--porcelain", *line_ranges, rev, "--", path)
    return Counter(m.group(1) for m in map(BLAME_HEADER.match, out.splitlines()) if m)


def removed_ranges(root: str, commit: HistoryCommit) -> dict[str, list[tuple[int, int]]]:
    """Old-side line ranges per file that commit changed or removed."""
    diff = run_git(root, "diff", "-U0", "--no-renames", commit.parents[0], commit.sha)
    ranges: dict[str, list[tuple[int, int]]] = defaultdict(list)
    path = None
    for line in diff.splitlines():
        if line.startswith("--- "):
            path = line[len("--- a/"):] if line.startswith("--- a/") else None
        elif path and (m := HUNK_HEADER.match(line)):
            start, count = int(m.group(1)), int(m.group(2) if m.group(2) is not None else 1)
            if count > 0:
                ranges[path].append((start, start + count - 1))
    return ranges


def area_of(path: str) -> str:
    return "/".join(path.split("/")[:-1][:2]) or "."


def family_of(card: str, parents: dict[str, str]) -> str:
    """The parent card; a card without one is its own family."""
    return parents.get(card) or card


def cites(text: str, card: str) -> bool:
    return re.search(rf"(?<![\w.-]){re.escape(card)}(?![\w-]|\.\w)", text) is not None


def measure_rework(root: str, head: str, cards: list[dict]) -> dict:
    """Per-card survival and rework with per-maker, per-card-type and per-file-area summaries."""
    history = read_history(root, head)
    if not history:
        raise ValueError(f"{head} has no history")
    head_time = history[-1].committed_at
    meta = {c["id"]: c for c in cards}
    parents = {c["id"]: c["parent"] for c in cards if c.get("parent")}

    commits_of: dict[str, list[HistoryCommit]] = defaultdict(list)
    for c in history:
        if (card := card_of(c)) is not None:
            commits_of[card].append(c)
    owner = {c.sha: card for card, commits in commits_of.items() for c in commits}
    landed = {card: max(c.committed_at for c in commits) for card, commits in commits_of.items()}
    shas_of = {card: {c.sha for c in commits} for card, commits in commits_of.items()}
    times = [c.committed_at for c in history]

    records: dict[tuple[str, str], FileRecord] = {}
    makers: dict[str, str] = {}
    for card, commits in commits_of.items():
        makers[card] = commits[-1].author
        card_type = meta.get(card, {}).get("issue_type") or "unknown"
        for c in commits:
            for path, adds in c.added.items():
                rec = records.setdefault((card, path), FileRecord(card, makers[card], card_type, area_of(path), 0))
                rec.added += adds

    for (card, path), rec in records.items():
        for days in SURVIVAL_DAYS:
            horizon = landed[card] + days * DAY_SECONDS
            if horizon > head_time:
                rec.survived[days] = None
                continue
            snapshot = history[bisect.bisect_right(times, horizon) - 1]
            origins = blame_origins(root, snapshot.sha, path)
            rec.survived[days] = sum(n for sha, n in origins.items() if sha in shas_of[card])
        if landed[card] + REWORK_DAYS * DAY_SECONDS > head_time:
            rec.reworked = rec.reworked_same_family = None

    for c in history:
        changer = owner.get(c.sha)
        if changer is None or not c.parents:
            continue
        for path, ranges in removed_ranges(root, c).items():
            for sha, n in blame_origins(root, c.parents[0], path, ranges).items():
                victim = owner.get(sha)
                rec = records.get((victim, path)) if victim not in (None, changer) else None
                if rec is None or rec.reworked is None or c.committed_at > landed[victim] + REWORK_DAYS * DAY_SECONDS:
                    continue
                rec.reworked += n
                if family_of(changer, parents) == family_of(victim, parents):
                    rec.reworked_same_family += n

    reverted: Counter[str] = Counter()
    for c in history:
        for sha in REVERTS_LINE.findall(c.body):
            if sha in owner:
                reverted[owner[sha]] += 1
    bug_cards = {
        card: sorted(
            b["id"] for b in cards
            if b.get("issue_type") == "bug" and b["id"] != card
            and cites(" ".join(str(b.get(k) or "") for k in ("title", "description")), card)
        )
        for card in commits_of
    }

    per_card = []
    for card in sorted(commits_of):
        mine = [r for (c, _), r in records.items() if c == card]
        per_card.append({
            "card": card, "maker": makers[card], "card_type": mine[0].card_type if mine else "unknown",
            "landed_at": landed[card], "added_lines": sum(r.added for r in mine),
            **summarize_files(mine, {card}, reverted, bug_cards),
        })
    groups = {}
    for grouping in GROUPINGS:
        by_key: dict[str, list[FileRecord]] = defaultdict(list)
        for rec in records.values():
            by_key[getattr(rec, grouping)].append(rec)
        groups[grouping] = {key: summarize_files(recs, {r.card for r in recs}, reverted, bug_cards)
                            for key, recs in sorted(by_key.items())}
    return {"head": history[-1].sha, "head_time": head_time, "cards": per_card, "groups": groups}


def ratio(part: int, whole: int) -> float | None:
    return part / whole if whole else None


def summarize_files(recs: list[FileRecord], cards: set[str], reverted: Counter[str], bug_cards: dict[str, list[str]]) -> dict:
    """Counts with their sample sizes; a share is None when its sample is empty."""
    out: dict = {"cards": len(cards), "added_lines": sum(r.added for r in recs)}
    for days in SURVIVAL_DAYS:
        sample = [r for r in recs if r.survived[days] is not None]
        survived = sum(r.survived[days] for r in sample)
        lines = sum(r.added for r in sample)
        out[f"survival_{days}d"] = {"cards": len({r.card for r in sample}), "lines": lines, "survived": survived, "share": ratio(survived, lines)}
    sample = [r for r in recs if r.reworked is not None]
    lines = sum(r.added for r in sample)
    out[f"rework_{REWORK_DAYS}d"] = {
        "cards": len({r.card for r in sample}), "lines": lines, "reworked": sum(r.reworked for r in sample),
        "reworked_same_family": sum(r.reworked_same_family for r in sample),
        "share": ratio(sum(r.reworked for r in sample), lines),
    }
    out["reverted_cards"] = sum(1 for c in cards if reverted[c])
    out["cards_with_follow_up_bugs"] = sum(1 for c in cards if bug_cards[c])
    return out


def percent(share: float | None) -> str:
    return "n/a" if share is None else f"{share:.0%}"


def render_markdown(result: dict) -> str:
    """Tables per grouping; every share is printed next to the lines and cards behind it."""
    lines = [
        f"# Survival and rework of landed cards at {result['head'][:12]}", "",
        "Descriptive counts: no benchmark exists, so read each share with its sample. n/a means no card was old enough.", "",
    ]
    for grouping in GROUPINGS:
        lines += [f"## By {grouping.replace('_', ' ')}", "",
                  "| " + grouping + " | cards | added lines | survived 7d | survived 30d | reworked 14d | by same family | reverted | with bug cards |",
                  "|---|---|---|---|---|---|---|---|---|"]
        for key, g in result["groups"][grouping].items():
            s7, s30, rw = g["survival_7d"], g["survival_30d"], g["rework_14d"]
            lines.append(
                f"| {key} | {g['cards']} | {g['added_lines']} | {percent(s7['share'])} of {s7['lines']} ({s7['cards']} cards)"
                f" | {percent(s30['share'])} of {s30['lines']} ({s30['cards']} cards)"
                f" | {percent(rw['share'])} of {rw['lines']} ({rw['cards']} cards) | {rw['reworked_same_family']} of {rw['reworked']}"
                f" | {g['reverted_cards']} | {g['cards_with_follow_up_bugs']} |")
        lines.append("")
    lines += ["## Per card", "", "| card | maker | type | added | survived 7d | survived 30d | reworked 14d | reverts | bug cards |", "|---|---|---|---|---|---|---|---|---|"]
    for c in result["cards"]:
        lines.append(
            f"| {c['card']} | {c['maker']} | {c['card_type']} | {c['added_lines']} | {percent(c['survival_7d']['share'])}"
            f" | {percent(c['survival_30d']['share'])} | {percent(c['rework_14d']['share'])} | {c['reverted_cards']} | {c['cards_with_follow_up_bugs']} |")
    return "\n".join(lines) + "\n"
