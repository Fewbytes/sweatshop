"""The per-language adapter interface and the types signals travel in.

An adapter declares which signals it computes, names the tools (with versions)
whose behaviour its numbers depend on, and returns for each declared signal
either a value or Unavailable with the reason it could not be computed.
"""

from dataclasses import dataclass
from typing import Protocol


@dataclass(frozen=True)
class Unavailable:
    """A signal that could not be computed; serialized as {"unavailable": reason}."""

    reason: str


SignalValue = int | float | str | list | dict | None | Unavailable
Signals = dict[str, SignalValue]


@dataclass(frozen=True)
class Commit:
    card: str | None  # the Bead trailer, None when the commit names no card
    deleted_lines: dict[str, int]  # every file touched -> lines it removed or replaced


@dataclass(frozen=True)
class History:
    """Commits in the window ending at the measured revision."""

    measured_at: str  # committer date of the revision: the clock of the run
    commits: list[Commit]


@dataclass
class AdapterResult:
    packages: dict[str, Signals]  # package directory -> its signals
    repo: Signals
    files: dict[str, Signals]  # repo-relative path -> its signals
    cochange_pairs: list[Signals]  # file pairs that change together, most frequent first
    hotspot_functions: list[Signals]  # riskiest functions, highest hotspot first
    module_edges: list[Signals]  # package pairs linked by an import or by co-change


class Adapter(Protocol):
    language: str
    package_signals: tuple[str, ...]  # every one must be a key of each package in the result
    repo_signals: tuple[str, ...]  # every one must be a key of the repo in the result

    def tool_versions(self) -> dict[str, str]:
        """Every tool or rule set the numbers depend on -> its version."""
        ...

    def measure(self, root: str, history: History) -> AdapterResult: ...
