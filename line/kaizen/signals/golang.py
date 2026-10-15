"""Static facts about Go source, extracted with a small lexer instead of a Go
toolchain so the signals are computed the same way on every station.

Everything here is an approximation chosen for determinism and speed: the
numbers are comparable between two runs of these scripts, not with other tools.
"""

import re
from dataclasses import dataclass, field

IDENT = re.compile(r"[A-Za-z_]\w*")
_TOKEN = re.compile(r"[A-Za-z_]\w*|&&|\|\||[{};]")


def scan(src: str) -> tuple[str, str]:
    """Return (code, blanked): src without comments, and the same with string
    and rune literal contents emptied. Newlines are kept so line numbers hold."""
    code: list[str] = []
    blanked: list[str] = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            newlines = src.count("\n", i, j)
            code.append("\n" * newlines)
            blanked.append("\n" * newlines)
            i = j
        elif c in "\"'`":
            j = i + 1
            while j < n and src[j] != c:
                j += 2 if src[j] == "\\" and c != "`" else 1
            j = min(j + 1, n)
            literal = src[i:j]
            code.append(literal)
            blanked.append(c + "\n" * literal.count("\n") + c)
            i = j
        else:
            code.append(c)
            blanked.append(c)
            i += 1
    return "".join(code), "".join(blanked)


_NESTING_KEYWORDS = {"if", "for", "switch", "select"}


@dataclass
class FunctionComplexity:
    name: str
    complexity: int


def function_complexities(blanked: str) -> list[FunctionComplexity]:
    """Cognitive complexity of each top-level func: +1 per if/for/switch/select
    plus the number of enclosing nested structures, +1 per else, +1 per goto,
    +1 per change of &&/|| operator inside a block. Function literals nest
    without adding to the score."""
    out: list[FunctionComplexity] = []
    for m in re.finditer(r"^func\s*(?:\([^)]*\)\s*)?(\w+)", blanked, re.M):
        start = blanked.find("{", m.end())
        if start < 0:
            continue
        out.append(FunctionComplexity(m.group(1), _body_complexity(blanked, start)))
    return out


def _body_complexity(src: str, open_brace: int) -> int:
    score = 0
    stack: list[bool] = []  # one entry per open brace: does it nest?
    pending_nest = False
    last_op = ""
    prev = ""
    depth_nesting = 0
    for tok in _TOKEN.finditer(src, open_brace):
        t = tok.group()
        if t == "{":
            stack.append(pending_nest)
            depth_nesting += pending_nest
            pending_nest = False
            last_op = ""
        elif t == "}":
            depth_nesting -= stack.pop()
            last_op = ""
            if not stack:
                break
        elif t in _NESTING_KEYWORDS:
            if prev != "else":
                score += 1 + depth_nesting
            pending_nest = True
        elif t == "else":
            score += 1
            pending_nest = True
        elif t == "func":
            pending_nest = True  # function literal: the declaration itself starts before open_brace
        elif t == "goto":
            score += 1
        elif t in ("&&", "||"):
            if t != last_op:
                score += 1
            last_op = t
        elif t == ";":
            last_op = ""
        prev = t
    return score


_IMPORT_BLOCK = re.compile(r"^import\s*\((.*?)^\)", re.M | re.S)
_IMPORT_SINGLE = re.compile(r'^import\s+(?:[\w.]+\s+)?"([^"]+)"', re.M)
_QUOTED = re.compile(r'"([^"]+)"')


def imports(code: str) -> list[str]:
    paths = _IMPORT_SINGLE.findall(code)
    for block in _IMPORT_BLOCK.findall(code):
        paths += _QUOTED.findall(block)
    return paths


_DECL_LINE = re.compile(r"^(?:func\s+(\w+)\s*[\[(]|(?:type|var|const)\s+(\w+))", re.M)
_DECL_GROUP = re.compile(r"^(?:type|var|const)\s*\((.*?)^\)", re.M | re.S)
_GROUP_MEMBER = re.compile(r"^\t(\w+)\b", re.M)
NOT_DEAD = re.compile(r"^(main|init|_|Test\w*|Benchmark\w*|Example\w*|Fuzz\w*)$")


def declared_symbols(blanked: str) -> list[str]:
    """Top-level function, type, var and const names; methods are skipped
    because interfaces can call them without a textual reference."""
    names = [a or b for a, b in _DECL_LINE.findall(blanked)]
    for group in _DECL_GROUP.findall(blanked):
        names += _GROUP_MEMBER.findall(group)
    return [n for n in names if not NOT_DEAD.match(n)]


@dataclass
class Idiom:
    concern: str
    name: str
    pattern: re.Pattern[str]


def _idiom(concern: str, name: str, pattern: str) -> Idiom:
    return Idiom(concern, name, re.compile(pattern, re.S))


# The literal table of idioms per concern. Pattern entropy is the Shannon
# entropy of the counts within one concern: 0 when one idiom is used, up when
# the code base mixes them.
IDIOMS = [
    _idiom("error-creation", "errorf-wrap", r'fmt\.Errorf\(\s*"[^"\\]*%w'),
    _idiom("error-creation", "errorf-plain", r'fmt\.Errorf\(\s*"(?![^"\\]*%w)'),
    _idiom("error-creation", "errors-new", r"\berrors\.New\("),
    _idiom("error-creation", "panic", r"\bpanic\("),
    _idiom("logging", "log-std", r"\blog\.(?:Print|Fatal|Panic)\w*\("),
    _idiom("logging", "slog", r"\bslog\.\w+\("),
    _idiom("logging", "stderr-print", r"\bfmt\.Fprint\w*\(os\.Stderr"),
    _idiom("logging", "stdout-print", r"\bfmt\.Print\w*\("),
    _idiom("logging", "logger-field", r"\.Log\.Print\w*\(|\blogf\("),
]


def idiom_counts(code: str) -> dict[tuple[str, str], int]:
    return {(i.concern, i.name): len(i.pattern.findall(code)) for i in IDIOMS}


@dataclass
class GoFile:
    path: str  # repo-relative
    is_test: bool
    imports: list[str]
    functions: list[FunctionComplexity]
    symbols: list[str]
    identifiers: list[str]
    idioms: dict[tuple[str, str], int]
    normalized_lines: list[str] = field(default_factory=list)


_BRACE_ONLY = re.compile(r"^[{}()\[\],;]*$")


def parse(path: str, src: str) -> GoFile:
    code, blanked = scan(src)
    lines = [re.sub(r"\s+", " ", ln).strip() for ln in code.splitlines()]
    return GoFile(
        path=path,
        is_test=path.endswith("_test.go"),
        imports=imports(code),
        functions=function_complexities(blanked),
        symbols=declared_symbols(blanked),
        identifiers=IDENT.findall(blanked),
        idioms=idiom_counts(code),
        normalized_lines=[ln for ln in lines if ln and not _BRACE_ONLY.match(ln)],
    )
