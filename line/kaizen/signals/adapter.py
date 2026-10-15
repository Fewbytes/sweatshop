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
class History:
    """Files touched by each commit in the window ending at the measured revision."""

    measured_at: str  # committer date of the revision: the clock of the run
    commits: list[list[str]]


@dataclass
class AdapterResult:
    packages: dict[str, Signals]  # package directory -> its signals
    repo: Signals


class Adapter(Protocol):
    language: str
    package_signals: tuple[str, ...]  # every one must be a key of each package in the result
    repo_signals: tuple[str, ...]  # every one must be a key of the repo in the result

    def tool_versions(self) -> dict[str, str]:
        """Every tool or rule set the numbers depend on -> its version."""
        ...

    def measure(self, root: str, history: History) -> AdapterResult: ...
