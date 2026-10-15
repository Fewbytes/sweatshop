"""Inline-SVG graph views for the status report: the module dependency graph
(imports against co-change) and a module's cohesion graph (symbols and the
references between them). Layout is a circle in sorted order, so the same
signals always draw the same picture.
"""

import html
import math

SIZE = 520
MAX_GRAPH_NODES = 40
# Neutral grey for imports, red dashed for co-change with no import. Dash as
# well as colour, so the two stay apart without colour vision.
IMPORT_STYLE = 'stroke="#667" '
HIDDEN_STYLE = 'stroke="#a4161a" stroke-dasharray="6 4" '
COMPONENT_COLOURS = ("#1f6feb", "#0a6b2d", "#b35900", "#7b2cbf", "#a4161a", "#007a87")


def _positions(nodes: list[str]) -> dict[str, tuple[float, float]]:
    radius = SIZE / 2 - 90
    return {
        n: (round(SIZE / 2 + radius * math.cos(2 * math.pi * i / len(nodes)), 1),
            round(SIZE / 2 + radius * math.sin(2 * math.pi * i / len(nodes)), 1))
        for i, n in enumerate(nodes)
    }


def _svg(body: list[str]) -> str:
    return f'<svg xmlns="http://www.w3.org/2000/svg" width="{SIZE}" height="{SIZE}" role="img">' + "".join(body) + "</svg>"


def _label(x: float, y: float, text: str) -> str:
    anchor = "start" if x >= SIZE / 2 else "end"
    dx = 8 if x >= SIZE / 2 else -8
    return f'<text x="{x + dx}" y="{y + 4}" font-size="10" text-anchor="{anchor}">{html.escape(text)}</text>'


def dependency_graph(edges: list[dict], nodes: list[str]) -> str:
    """Edges are signals.json `module_edges`. An import edge is a solid line
    whose width is the number of import statements; a co-change edge without
    an import is dashed red with its width the number of shared commits.
    Co-change edges of modules that also import each other are not drawn."""
    where = _positions(nodes)
    body = []
    for e in edges:
        if e["a"] not in where or e["b"] not in where:
            continue
        (x1, y1), (x2, y2) = where[e["a"]], where[e["b"]]
        if e["imports"]:
            style, weight = IMPORT_STYLE, e["imports"]
        elif e["hidden"]:
            style, weight = HIDDEN_STYLE, e["cochange"]
        else:
            continue
        body.append(
            f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" {style}stroke-width="{min(1 + weight / 2, 8):g}" opacity="0.7">'
            f'<title>{html.escape(e["a"])} – {html.escape(e["b"])}: {e["imports"]} imports, {e["cochange"]} co-changes</title></line>'
        )
    for n, (x, y) in where.items():
        body.append(f'<circle cx="{x}" cy="{y}" r="4" fill="#223"/>' + _label(x, y, n))
    return _svg(body)


def cohesion_graph(edges: list[list[str]]) -> tuple[str, int]:
    """(svg, symbols drawn) for one module's symbol references. Symbols in
    one connected component share a colour. Past MAX_GRAPH_NODES only the
    most-connected symbols are drawn; a symbol that references no other symbol
    of the module and is referenced by none is not drawn."""
    degree: dict[str, int] = {}
    for a, b in edges:
        degree[a] = degree.get(a, 0) + 1
        degree[b] = degree.get(b, 0) + 1
    shown = sorted(sorted(degree, key=lambda n: (-degree[n], n))[:MAX_GRAPH_NODES])
    drawn = [(a, b) for a, b in edges if a in shown and b in shown]
    where = _positions(shown)
    parent = {n: n for n in shown}

    def find(n: str) -> str:
        while parent[n] != n:
            n = parent[n]
        return n

    for a, b in drawn:
        parent[find(a)] = find(b)
    roots = sorted({find(n) for n in shown})
    body = [
        f'<line x1="{where[a][0]}" y1="{where[a][1]}" x2="{where[b][0]}" y2="{where[b][1]}" stroke="#667" opacity="0.5"/>'
        for a, b in drawn
    ]
    for n, (x, y) in where.items():
        colour = COMPONENT_COLOURS[roots.index(find(n)) % len(COMPONENT_COLOURS)]
        body.append(f'<circle cx="{x}" cy="{y}" r="4" fill="{colour}"/>' + _label(x, y, n))
    return _svg(body), len(shown)
