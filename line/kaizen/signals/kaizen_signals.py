# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Kaizen code-health signals (line/guidance/kaizen.md, section 3).

  measure --root DIR --rev REV --out signals.json [--runs runs.json]
  report  --signals signals.json --out report.html [--previous p.json] [--after a.json]
          [--target PKG --category P2 --signal clones --why TEXT]
  mutation --root DIR --language go|python|typescript --out mutation.json [--cap-seconds N] SCOPE...
  rework --root DIR --cards cards.json --out rework.json [--markdown rework.md] [--rev REV]
  calibrate --root DIR --cards cards.json --out calibration.json [--markdown calibration.md] [--rev REV]
  modules --signals signals.json --map docs/modules.json
  modules-check --root DIR --map docs/modules.json
  gate    --root DIR --base REV [--thresholds kaizen-gate.json] [--baseline accepted.json]
  contracts --root DIR --base REV --card-kind refactor|behaviour|other [--message-file MSG]
  delta   --before b.json --after a.json --package PKG --signal NAME

Run with: uv run --no-project --with-requirements line/kaizen/signals/requirements.txt \
          line/kaizen/signals/kaizen_signals.py ...
requirements.txt pins the tree-sitter grammars by checksum.
"""

import argparse
import json
import sys

import calibrate
import contract_gate
import gate
import measure
import modules
import mutation
import render
import rework


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


def cmd_mutation(args: argparse.Namespace) -> int:
    signals = mutation.mutation_signals(args.root, args.language, tuple(args.scope), args.cap_seconds)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(measure.dumps(signals))
    return 0


def cmd_rework(args: argparse.Namespace) -> int:
    result = rework.measure_rework(args.root, args.rev, load(args.cards))
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(measure.dumps(result))
    if args.markdown:
        with open(args.markdown, "w", encoding="utf-8") as f:
            f.write(rework.render_markdown(result))
    return 0


def cmd_calibrate(args: argparse.Namespace) -> int:
    result = calibrate.run_calibration(args.root, args.rev, load(args.cards))
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(measure.dumps(result))
    if args.markdown:
        with open(args.markdown, "w", encoding="utf-8") as f:
            f.write(calibrate.render_markdown(result))
    return 0


def cmd_modules(args: argparse.Namespace) -> int:
    try:
        committed = load(args.map)
    except FileNotFoundError:
        committed = None
    with open(args.map, "w", encoding="utf-8") as f:
        f.write(measure.dumps(modules.update_map(load(args.signals), committed)))
    return 0


def cmd_modules_check(args: argparse.Namespace) -> int:
    problems = modules.check_map(args.root, load(args.map))
    for problem in problems:
        print(problem, file=sys.stderr)
    if problems:
        print(f"{args.map} disagrees with the tree: regenerate it with the `modules` subcommand (kaizen.md, section 3)", file=sys.stderr)
    return 1 if problems else 0


def cmd_report(args: argparse.Namespace) -> int:
    pick_fields = [args.target, args.category, args.signal, args.why]
    if any(pick_fields) and not all(pick_fields):
        sys.exit("report: --target, --category, --signal and --why go together")
    pick = render.Pick(*pick_fields) if all(pick_fields) else None
    page = render.render_report(load(args.signals), load(args.previous), load(args.after), pick)
    with open(args.out, "w", encoding="utf-8") as f:
        f.write(page)
    return 0


def cmd_gate(args: argparse.Namespace) -> int:
    """Print the signal deltas of the files changed against --base; exit 1 on a finding over threshold."""
    report, findings = gate.run_gate(
        args.root, args.base, gate.load_thresholds(args.thresholds), gate.load_baseline(args.baseline)
    )
    for line in report:
        print(line)
    for finding in findings:
        print(f"gate: {finding.message} [{finding.key}]", file=sys.stderr)
    return 1 if findings else 0


def cmd_contracts(args: argparse.Namespace) -> int:
    """List the guards, defaults, fallbacks and error contracts the working tree removed or altered
    against --base; exit 1 when the card kind and commit message do not allow that."""
    removed = contract_gate.removed_contracts(args.root, args.base)
    message = ""
    if args.message_file:
        with open(args.message_file, encoding="utf-8") as f:
            message = f.read()
    for item in removed:
        print(f"{item.path}: {item.rule} removed or altered: {' '.join(item.text.split())}")
    reasons = contract_gate.judge(removed, contract_gate.CardKind(args.card_kind), message)
    for reason in reasons:
        print(f"contracts: {reason}", file=sys.stderr)
    return 1 if reasons else 0


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

    mu = sub.add_parser("mutation", help="mutation score of the files or package a change will touch")
    mu.add_argument("--root", required=True)
    mu.add_argument("--language", required=True, choices=sorted(mutation.TOOLS))
    mu.add_argument("--out", required=True)
    mu.add_argument("--cap-seconds", type=float, default=mutation.DEFAULT_CAP_SECONDS)
    mu.add_argument("scope", nargs="+", help="Go package directory, or Python/TypeScript files, relative to root")
    mu.set_defaults(func=cmd_mutation)

    rw = sub.add_parser("rework", help="survival, rework and reverts of landed cards' code")
    rw.add_argument("--root", required=True)
    rw.add_argument("--rev", default="HEAD")
    rw.add_argument("--cards", required=True, help="`sweatshop line cards list` output saved as JSON: card type, parent and bug cards")
    rw.add_argument("--out", required=True)
    rw.add_argument("--markdown", help="also write the tables as Markdown")
    rw.set_defaults(func=cmd_rework)

    ca = sub.add_parser("calibrate", help="which file signals predicted where closed bug cards were fixed")
    ca.add_argument("--root", required=True)
    ca.add_argument("--rev", default="HEAD")
    ca.add_argument("--cards", required=True, help="`sweatshop line cards list --status closed` output saved as JSON")
    ca.add_argument("--out", required=True)
    ca.add_argument("--markdown", help="also write the table as Markdown")
    ca.set_defaults(func=cmd_calibrate)

    mo = sub.add_parser("modules", help="write or refresh the committed module map")
    mo.add_argument("--signals", required=True)
    mo.add_argument("--map", required=True, help="the map to create, or to refresh keeping entries that are not stale")
    mo.set_defaults(func=cmd_modules)

    mc = sub.add_parser("modules-check", help="exit 1 when the committed module map disagrees with the tree")
    mc.add_argument("--root", required=True)
    mc.add_argument("--map", required=True)
    mc.set_defaults(func=cmd_modules_check)

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

    g = sub.add_parser("gate", help="exit 1 when the working tree is worse than a revision in the files it changed")
    g.add_argument("--root", required=True)
    g.add_argument("--base", required=True, help="revision to compare with, normally HEAD")
    g.add_argument("--thresholds", help="JSON of gate.Thresholds fields; defaults allow no growth")
    g.add_argument("--baseline", help="JSON list of finding keys accepted as existing debt")
    g.set_defaults(func=cmd_gate)

    c = sub.add_parser("contracts", help="exit 1 when a card that must not change behaviour removes or alters a guard, default, fallback or error contract")
    c.add_argument("--root", required=True)
    c.add_argument("--base", required=True, help="revision to compare with, normally HEAD")
    c.add_argument("--card-kind", required=True, choices=[k.value for k in contract_gate.CardKind],
                   help="refactor: labelled refactor or from Kaizen; behaviour: changes behaviour on purpose; other: needs a justification")
    c.add_argument("--message-file", help=f"commit message; a '{contract_gate.JUSTIFICATION_PREFIX} <why>' line justifies the change")
    c.set_defaults(func=cmd_contracts)

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
