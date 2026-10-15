# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Kaizen code-health signals (line/guidance/kaizen.md, section 3).

  measure --root DIR --rev REV --out signals.json [--runs runs.json]
  report  --signals signals.json --out report.html [--previous p.json] [--after a.json]
          [--target PKG --category P2 --signal clones --why TEXT]
  delta   --before b.json --after a.json --package PKG --signal NAME

Run with: uv run --no-project line/kaizen/signals/kaizen_signals.py ...
"""

import argparse
import json
import sys

import measure
import render


def load(path: str | None) -> dict | None:
    if path is None:
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def cmd_measure(args: argparse.Namespace) -> int:
    signals = measure.measure(args.root, args.rev, load(args.runs))
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(measure.dumps(signals))
    return 0


def cmd_report(args: argparse.Namespace) -> int:
    pick_fields = [args.target, args.category, args.signal, args.why]
    if any(pick_fields) and not all(pick_fields):
        sys.exit("report: --target, --category, --signal and --why go together")
    pick = render.Pick(*pick_fields) if all(pick_fields) else None
    page = render.render_report(load(args.signals), load(args.previous), load(args.after), pick)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(page)
    return 0


def cmd_delta(args: argparse.Namespace) -> int:
    """Print the commit message's Signals line; exit 1 if the signal did not drop."""
    before, after = load(args.before), load(args.after)
    mismatch = render.baseline_mismatch(before, after)
    if mismatch:
        print(f"delta refuses to compare: {mismatch}", file=sys.stderr)
        return 1
    old = render.signal_value(before, args.package, args.signal)
    new = render.signal_value(after, args.package, args.signal)
    if old is None or new is None:
        print(f"{args.signal} of {args.package} is unavailable in one of the runs", file=sys.stderr)
        return 1
    print(f"Signals:   {args.package} {args.signal}: {old:g} → {new:g}")
    for line in render.regressions(before, after, args.package, args.signal):
        print(f"warning: also worse: {line}", file=sys.stderr)
    if not new < old:
        print(f"{args.signal} of {args.package} did not move down: the change is not submitted", file=sys.stderr)
        return 1
    return 0


def main(argv: list[str]) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="command", required=True)

    m = sub.add_parser("measure")
    m.add_argument("--root", required=True)
    m.add_argument("--rev", required=True, help="revision whose history gives churn and co-change")
    m.add_argument("--out", required=True)
    m.add_argument("--runs", help="GET /v1/runs saved as JSON, for CI runtime")
    m.set_defaults(func=cmd_measure)

    r = sub.add_parser("report")
    r.add_argument("--signals", required=True)
    r.add_argument("--out", required=True)
    r.add_argument("--previous")
    r.add_argument("--after")
    r.add_argument("--target")
    r.add_argument("--category")
    r.add_argument("--signal")
    r.add_argument("--why")
    r.set_defaults(func=cmd_report)

    d = sub.add_parser("delta")
    d.add_argument("--before", required=True)
    d.add_argument("--after", required=True)
    d.add_argument("--package", required=True, help="package directory, or . for the whole repo")
    d.add_argument("--signal", required=True)
    d.set_defaults(func=cmd_delta)

    args = p.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
