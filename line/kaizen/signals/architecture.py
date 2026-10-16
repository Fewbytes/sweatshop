"""Architecture decay over history (kaizen.md, section 3): propagation cost,
import-cycle count and largest cycle at weekly sampled revisions, and for each
current cycle the commit that first formed it.

The import graph of a historical revision is read from a `git archive` of it,
never from the working tree. Cycle origins are found by bisecting the first-parent
history, which assumes a cycle that exists now has existed continuously since
it formed; a cycle that was broken and re-formed is dated to its first formation
only if that formation held at every bisection probe.
"""

import subprocess
import tempfile
from datetime import datetime

from adapter import Adapter, Signals
from source_adapter import import_cycles

MAX_TREND_SAMPLES = 26  # weekly samples, newest first, so a run costs a bounded number of tree parses

ImportGraph = dict[str, frozenset[str]]


def git(root: str, *args: str) -> str:
    return subprocess.run(["git", "-C", root, *args], check=True, capture_output=True, text=True).stdout


def propagation_cost(graph: ImportGraph) -> float:
    """MacCormack et al.: the share of package pairs (self included) where a change to one can reach the other
    through imports. 0 when there are no packages."""
    if not graph:
        return 0.0
    reachable = 0
    for start in graph:
        seen = {start}
        todo = [start]
        while todo:
            for target in graph[todo.pop()]:
                if target not in seen:
                    seen.add(target)
                    todo.append(target)
        reachable += len(seen)
    return round(reachable / len(graph) ** 2, 3)


def graph_at(adapters: tuple[Adapter, ...], root: str, rev: str) -> ImportGraph:
    """The package import graph of the tree at rev, over every language's packages."""
    archive = subprocess.run(["git", "-C", root, "archive", rev], check=True, capture_output=True).stdout
    graph: ImportGraph = {}
    with tempfile.TemporaryDirectory() as tree:
        subprocess.run(["tar", "-x", "-m", "-C", tree], input=archive, check=True)
        for adapter in adapters:
            graph.update(adapter.import_graph(tree))
    return graph


def sampled_revisions(root: str, rev: str) -> list[tuple[str, str]]:
    """(sha, committer date) of the newest first-parent commit of each ISO week, oldest first, at most
    MAX_TREND_SAMPLES of the latest weeks. rev itself is always the last."""
    newest_per_week: dict[tuple[int, int], tuple[str, str]] = {}
    for line in git(root, "log", "--first-parent", "--format=%H %cI", rev).splitlines():
        sha, date = line.split(" ")
        iso = datetime.fromisoformat(date).isocalendar()
        newest_per_week.setdefault((iso.year, iso.week), (sha, date))
    return list(newest_per_week.values())[:MAX_TREND_SAMPLES][::-1]


def trend(adapters: tuple[Adapter, ...], root: str, rev: str) -> list[Signals]:
    samples = []
    for sha, date in sampled_revisions(root, rev):
        graph = graph_at(adapters, root, sha)
        cycles = import_cycles(graph)
        samples.append({
            "commit": sha,
            "date": date,
            "packages": len(graph),
            "propagation_cost": propagation_cost(graph),
            "import_cycles": len(cycles),
            "largest_cycle": max((len(c) for c in cycles), default=0),
        })
    return samples


def cycle_origins(adapters: tuple[Adapter, ...], root: str, rev: str) -> list[Signals]:
    """For each import cycle at rev: its members and the earliest first-parent commit from which all of them
    are in one cycle at every probed later revision, with that commit's date and Bead card (None when it names none)."""
    current = import_cycles(graph_at(adapters, root, rev))
    commits = git(root, "rev-list", "--first-parent", "--reverse", rev).split()
    graphs: dict[str, ImportGraph] = {}

    def formed(members: tuple[str, ...], sha: str) -> bool:
        if sha not in graphs:
            graphs[sha] = graph_at(adapters, root, sha)
        return any(set(members) <= set(cycle) for cycle in import_cycles(graphs[sha]))

    origins = []
    for members in current:
        low, high = 0, len(commits) - 1  # commits[high] is rev, where the cycle exists
        while low < high:
            middle = (low + high) // 2
            if formed(members, commits[middle]):
                high = middle
            else:
                low = middle + 1
        date, card = git(root, "log", "-1", "--format=%cI%n%(trailers:key=Bead,valueonly,separator=%x2C)", commits[low]).split("\n")[:2]
        origins.append({"members": list(members), "commit": commits[low], "date": date, "card": card.strip() or None})
    return origins
