"""The module map committed to the repo (docs/modules.json): one compact entry
per package, so every agent sees what a module is for, what it exports, who
depends on it and how healthy it is without querying the line store.

Two kinds of field, because only one kind can be checked against a tree:
  tree fields     purpose, entry points, imports in and out, cohesion. A pure
                  function of the files; `check` fails when the map disagrees.
  history fields  hotspot rank, hidden-coupling ratio, measured revision. They
                  move with every commit, so `update` rewrites them only when
                  a tree field changed or one moved past a threshold below;
                  otherwise the committed entry stays byte for byte and master
                  is not churned.
"""

from adapter import History, Signals
from measure import ADAPTERS

SCHEMA = 1
MAX_ENTRY_POINTS = 12  # listed alphabetically; entry_point_count has the total
RANK_SHIFT = 5  # hotspot rank change that makes a module's entry stale
HIDDEN_RATIO_SHIFT = 0.2  # hidden-coupling ratio change that makes it stale
TREE_FIELDS = ("purpose", "entry_points", "entry_point_count", "imports", "imported_by", "lcom4")


def module_entries(packages: dict[str, Signals], commit: str) -> dict[str, Signals]:
    """Map entries from the `packages` of a signals.json measured at commit."""
    imported_by: dict[str, list[str]] = {pkg: [] for pkg in packages}
    for pkg, signals in packages.items():
        for target in signals["imports"]:
            imported_by.setdefault(target, []).append(pkg)
    return {
        pkg: {
            "purpose": signals["purpose"],
            "entry_points": signals["entry_points"][:MAX_ENTRY_POINTS],
            "entry_point_count": len(signals["entry_points"]),
            "imports": sorted(signals["imports"]),
            "imported_by": sorted(imported_by[pkg]),
            "lcom4": signals["lcom4"],
            "hotspot_rank": signals["hotspot_rank"],
            "hidden_coupling_ratio": signals["hidden_coupling_ratio"],
            "measured_at_commit": commit,
        }
        for pkg, signals in sorted(packages.items())
    }


def _stale(committed: Signals, fresh: Signals) -> bool:
    return (
        any(committed[f] != fresh[f] for f in TREE_FIELDS)
        or abs(committed["hotspot_rank"] - fresh["hotspot_rank"]) >= RANK_SHIFT
        or abs(committed["hidden_coupling_ratio"] - fresh["hidden_coupling_ratio"]) >= HIDDEN_RATIO_SHIFT
    )


def update_map(signals: dict, committed: dict | None) -> dict:
    """The map for `signals`, keeping every committed entry that is not stale."""
    old = (committed or {}).get("modules", {})
    fresh = module_entries(signals["packages"], signals["commit"])
    kept = {pkg: old[pkg] if pkg in old and not _stale(old[pkg], entry) else entry for pkg, entry in fresh.items()}
    return {"schema": SCHEMA, "modules": kept}


def tree_entries(root: str) -> dict[str, Signals]:
    """Entries computed from the files alone: history fields are placeholders."""
    packages: dict[str, Signals] = {}
    for adapter in ADAPTERS:
        packages.update(adapter.measure(root, History("", [])).packages)
    return module_entries(packages, "")


def check_map(root: str, committed: dict) -> list[str]:
    """What is wrong with a committed map for the tree at root; empty when it agrees."""
    if committed.get("schema") != SCHEMA:
        return [f"map schema is {committed.get('schema')!r}, want {SCHEMA}"]
    old = committed["modules"]
    fresh = tree_entries(root)
    problems = [f"{pkg}: missing from the map" for pkg in fresh if pkg not in old]
    problems += [f"{pkg}: in the map but not in the tree" for pkg in old if pkg not in fresh]
    for pkg in fresh.keys() & old.keys():
        problems += [f"{pkg}: {f} is {old[pkg][f]!r}, tree has {fresh[pkg][f]!r}" for f in TREE_FIELDS if old[pkg][f] != fresh[pkg][f]]
    return sorted(problems)

