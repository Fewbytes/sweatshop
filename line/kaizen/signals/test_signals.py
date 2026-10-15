import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest

import kaizen_signals
import lang_go
import lang_python
import lang_typescript
import measure
import render
import source_adapter
import syntax
from adapter import Unavailable

BRANCHY = """package a

func Branchy(a, b bool) int {
	if a && b {
		for i := 0; i < 3; i++ {
			if a {
				return i
			}
		}
	} else {
		return 1
	}
	return 0
}
"""
CLONE_BODY = "".join(f"\tx{i} := compute(alpha, beta, gamma, delta, {i})\n\tuse(x{i})\n" for i in range(6))


def git(root, *args, date="2026-10-01T12:00:00+00:00"):
    env = {**os.environ, "GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": date,
           "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}
    subprocess.run(["git", "-C", root, *args], check=True, capture_output=True, env=env)


def parse(spec, text, path="x"):
    return syntax.parse_source(spec, path, text.encode())


def complexities(spec, text):
    return [(f.name, f.complexity) for f in parse(spec, text).functions]


class GoSourceFacts(unittest.TestCase):
    def test_cognitive_complexity_counts_branches_nesting_else_and_operators(self):
        self.assertEqual(complexities(lang_go.GO, BRANCHY), [("Branchy", 8)])

    def test_strings_and_comments_hold_no_structure(self):
        src = 'package a\nfunc F() string {\n\t// if for\n\treturn "if { for"\n}\n'
        parsed = parse(lang_go.GO, src)
        self.assertEqual([(f.name, f.complexity) for f in parsed.functions], [("F", 0)])
        self.assertNotIn("for", parsed.identifiers)

    def test_each_case_clause_scores_its_own_operator_run(self):
        src = "package a\nfunc F(a, b bool) int {\n\tswitch {\n\tcase a || b:\n\t\treturn 1\n\tcase a || b:\n\t\treturn 2\n\t}\n\treturn 0\n}\n"
        self.assertEqual(complexities(lang_go.GO, src), [("F", 3)])

    def test_function_literals_nest_without_scoring(self):
        src = "package a\nfunc F(a bool) {\n\tg := func() {\n\t\tif a {\n\t\t}\n\t}\n\tg()\n}\n"
        self.assertEqual(complexities(lang_go.GO, src), [("F", 2)])

    def test_package_level_function_literals_are_reported_as_anonymous(self):
        self.assertEqual(complexities(lang_go.GO, "package a\nvar f = func(a bool) {\n\tif a {\n\t}\n}\n"),
                         [(syntax.ANONYMOUS_FUNCTION, 1)])

    def test_imports_read_single_and_block_forms(self):
        parsed = parse(lang_go.GO, 'package a\nimport "os"\nimport (\n\t"fmt"\n\tx "m/b"\n)\n')
        self.assertEqual(sorted(parsed.imports), ["fmt", "m/b", "os"])

    def test_declared_symbols_skip_methods_and_entry_points(self):
        parsed = parse(
            lang_go.GO,
            "package a\nfunc Used() {}\nfunc (t T) Method() {}\nfunc main() {}\n"
            "type T struct{}\nconst (\n\tOne = 1\n\ttwo = 2\n)\nvar v int\nfunc TestX(t *testing.T) {}\n",
        )
        self.assertEqual(sorted(parsed.symbols), ["One", "T", "Used", "two", "v"])

    def test_idioms_are_counted_per_call_not_per_text(self):
        src = (
            'package a\nimport ("errors"; "fmt")\n'
            'func F(e error) error {\n\t_ = "errors.New(x)"\n\tfmt.Println("a")\n\tpanic(e)\n'
            '\t_ = errors.New("x")\n\treturn fmt.Errorf("w: %w", e)\n}\n'
        )
        counts = {k: n for k, n in parse(lang_go.GO, src).idioms.items() if n}
        self.assertEqual(counts, {
            ("error-creation", "errorf-wrap"): 1, ("error-creation", "errors-new"): 1,
            ("error-creation", "panic"): 1, ("logging", "stdout-print"): 1,
        })

    def test_tokens_ignore_comments_and_layout(self):
        a = parse(lang_go.GO, "package a\nfunc F() { x := 1 } // one\n").tokens
        b = parse(lang_go.GO, "package a\n\nfunc F() {\n\t/* two */\n\tx := 1\n}\n").tokens
        self.assertEqual(a, b)


PYTHON_BRANCHY = """import os
from . import sibling
from ..up.mod import thing


def branchy(a, b):
    if a and b:
        for i in range(3):
            if a:
                return i
    elif b or a:
        return 1
    else:
        return 0


class C:
    def method(self, x):
        return [y for y in x if y] if x else None


X = 1
"""


class PythonSourceFacts(unittest.TestCase):
    def test_cognitive_complexity_counts_branches_nesting_else_and_operators(self):
        # if +1, and +1, for +2, nested if +3, elif +1, or +1, else +1
        self.assertEqual(complexities(lang_python.PYTHON, PYTHON_BRANCHY), [("branchy", 10), ("method", 1)])

    def test_imports_keep_relative_prefixes(self):
        self.assertEqual(parse(lang_python.PYTHON, PYTHON_BRANCHY).imports, ["os", ".", "..up.mod"])

    def test_declared_symbols_are_top_level_functions_classes_and_assignments(self):
        parsed = parse(lang_python.PYTHON, PYTHON_BRANCHY + "\ndef main():\n    pass\n\n\n@deco\ndef decorated():\n    pass\n")
        self.assertEqual(sorted(parsed.symbols), ["C", "X", "branchy", "decorated"])

    def test_idioms(self):
        src = "import logging, sys\nlogging.info('a')\nprint('b')\nsys.exit(1)\nraise ValueError('c')\n"
        counts = {k: n for k, n in parse(lang_python.PYTHON, src).idioms.items() if n}
        self.assertEqual(counts, {
            ("logging", "logging-module"): 1, ("logging", "print"): 1,
            ("error-creation", "sys-exit"): 1, ("error-creation", "raise"): 1,
        })

    def test_test_files_are_recognised_by_name(self):
        self.assertEqual(
            [lang_python.PYTHON.is_test(p) for p in ("a/test_x.py", "a/x_test.py", "a/conftest.py", "a/x.py")],
            [True, True, True, False],
        )

    def test_relative_and_absolute_imports_resolve_to_package_directories(self):
        resolve = lang_python.python_import_resolver("", {"pkg/a.py", "pkg/sub/mod.py", "app.py"})
        self.assertEqual(
            [resolve("pkg/a.py", i) for i in (".sub", "..", ".sub.mod", "pkg.sub", "os", "app")],
            ["pkg/sub", ".", "pkg/sub", "pkg/sub", None, "."],
        )


TS_BRANCHY = """import { a } from "./lib/a";
export * from "../up";

export function branchy(x: number, y: boolean): number {
  if (x > 1 && y) {
    for (const i of [1, 2]) {
      if (y) {
        return i;
      }
    }
  } else if (y || x) {
    return 1;
  } else {
    return 0;
  }
  return x ?? 0;
}

export const arrow = (v: boolean) => (v ? 1 : 2);

class K {
  method(v: boolean) {
    return v;
  }
}
"""


class TypeScriptSourceFacts(unittest.TestCase):
    def test_cognitive_complexity_counts_branches_nesting_else_and_operators(self):
        # if +1, && +1, for +2, nested if +3, else-if +1 (else) +1 (||), else +1, ?? +1
        self.assertEqual(
            complexities(lang_typescript.TYPESCRIPT, TS_BRANCHY),
            [("branchy", 11), ("arrow", 1), ("method", 0)],
        )

    def test_imports_and_reexports_are_read_without_quotes(self):
        self.assertEqual(parse(lang_typescript.TYPESCRIPT, TS_BRANCHY).imports, ["./lib/a", "../up"])

    def test_declared_symbols_include_exported_and_plain_declarations(self):
        self.assertEqual(
            sorted(parse(lang_typescript.TYPESCRIPT, TS_BRANCHY + "type T = 1;\ninterface I {}\n").symbols),
            ["I", "K", "T", "arrow", "branchy"],
        )

    def test_idioms(self):
        src = "console.log('a');\nlogger.info('b');\nthrow new TypeError('c');\nPromise.reject(1);\nprocess.stderr.write('d');\n"
        counts = {k: n for k, n in parse(lang_typescript.TYPESCRIPT, src).idioms.items() if n}
        self.assertEqual(counts, {
            ("logging", "console"): 1, ("logging", "logger-method"): 1, ("logging", "process-stream"): 1,
            ("error-creation", "throw-new-error"): 1, ("error-creation", "promise-reject"): 1,
        })

    def test_relative_imports_resolve_to_the_directory_of_the_file_or_folder(self):
        resolve = lang_typescript.typescript_import_resolver("", {"src/x.ts", "src/y.ts", "src/lib/a.ts", "main.ts"})
        self.assertEqual(
            [resolve("src/x.ts", i) for i in ("./lib", "./lib/a", "../other", "./y.js", "react", "../main")],
            ["src/lib", "src/lib", None, "src", None, "."],
        )


class MeasureRepo(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = self.tmp.name
        git(self.root, "init", "-q", "--initial-branch=master")
        self.write("go.mod", "module example.com/m\n")
        self.write("a/a.go", BRANCHY + "\nfunc unusedHelper() {}\n")
        self.write("b/b.go", "package b\n\nimport \"example.com/m/a\"\n\nvar _ = a.Branchy\n")
        self.write("c/c.go", "package c\n\nfunc C() {}\n")
        self.commit("one")
        self.write("c/c.go", "package c\n\nfunc C() { _ = 1 }\n")
        self.write("a/a.go", BRANCHY + "\nfunc unusedHelper() { _ = 1 }\n")
        self.commit("two")

    def write(self, rel, text):
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(text)

    def commit(self, msg):
        git(self.root, "add", "-A")
        git(self.root, "commit", "-q", "-m", msg)

    def measure(self):
        return measure.measure(self.root, "HEAD", None)

    def test_hotspot_is_churn_times_complexity_and_ranks_packages(self):
        s = self.measure()
        a = s["packages"]["a"]
        self.assertEqual((a["churn"], a["complexity"], a["hotspot"], a["hotspot_rank"]), (2, 8, 16, 1))
        self.assertEqual(s["repo"]["hotspot"], 16)

    def test_dead_symbols_are_names_used_nowhere_else_in_the_module(self):
        a = self.measure()["packages"]["a"]
        self.assertEqual((a["dead_symbols"], a["dead_symbol_names"]), (1, ["unusedHelper"]))

    def test_hidden_coupling_counts_cochanging_packages_without_an_import(self):
        s = self.measure()
        self.assertEqual((s["repo"]["cochange_pairs"], s["repo"]["hidden_pairs"]), (1, 1))  # only a and c changed twice
        self.assertEqual(s["packages"]["c"]["hidden_coupling_ratio"], 1.0)
        self.assertEqual(s["packages"]["b"]["cochange_pairs"], 0)

    def test_cochanging_packages_with_an_import_are_not_hidden(self):
        self.write("a/a.go", BRANCHY + "\nfunc unusedHelper() { _ = 2 }\n")
        self.write("b/b.go", 'package b\n\nimport "example.com/m/a"\n\nvar _ = a.Branchy // touched\n')
        self.commit("three")
        s = self.measure()
        self.assertEqual(s["packages"]["b"]["cochange_pairs"], 1)
        self.assertEqual(s["packages"]["b"]["hidden_pairs"], 0)

    def test_clones_count_duplicated_blocks_once_per_fragment(self):
        self.write("c/dup1.go", "package c\n\nfunc One() {\n" + CLONE_BODY + "}\n")
        self.write("c/dup2.go", "package c\n\nfunc Two() {\n" + CLONE_BODY + "}\n")
        self.assertEqual(self.measure()["packages"]["c"]["clones"], 2)

    def test_python_and_typescript_packages_are_measured_beside_go_with_prefixed_repo_signals(self):
        self.write("py/mod.py", "def f(a, b):\n    if a and b:\n        return 1\n")
        self.write("web/app.ts", "export function g(a: boolean) {\n  if (a) {\n    return 1;\n  }\n}\n")
        self.commit("three")
        s = self.measure()
        self.assertEqual((s["packages"]["py"]["complexity"], s["packages"]["web"]["complexity"]), (2, 1))
        self.assertEqual((s["repo"]["python.complexity"], s["repo"]["typescript.complexity"], s["repo"]["complexity"]), (2, 1, 8))

    def test_two_languages_in_one_directory_fail_loudly(self):
        self.write("a/tool.py", "x = 1\n")
        with self.assertRaisesRegex(ValueError, r"python adapter .* both claim packages \['a'\]"):
            self.measure()

    def test_measuring_twice_gives_identical_output(self):
        self.assertEqual(measure.dumps(self.measure()), measure.dumps(self.measure()))

    def test_edit_to_tree_keeps_history_signals_and_moves_complexity(self):
        before = self.measure()
        self.write("a/a.go", "package a\n\nfunc Branchy() int { return 0 }\n")
        after = self.measure()
        self.assertEqual(before["packages"]["a"]["churn"], after["packages"]["a"]["churn"])
        self.assertEqual(after["packages"]["a"]["hotspot"], 0)

    def test_ci_runtime_is_the_median_of_passing_runs(self):
        runs = {"runs": [{"passed": True, "duration_ms": 30}, {"passed": False, "duration_ms": 999},
                         {"passed": True, "duration_ms": 10}, {"passed": True, "duration_ms": 20}]}
        self.assertEqual(measure.ci_runtime(runs), {"ci_runs": 3, "ci_median_ms": 20})

    def test_ci_runtime_is_unavailable_with_the_reason_when_no_runs_given(self):
        self.assertEqual(measure.ci_runtime(None)["ci_median_ms"], Unavailable("no CI runs file given"))

    def test_signals_record_schema_2_and_every_tool_version(self):
        s = self.measure()
        self.assertEqual(s["schema"], 2)
        self.assertEqual(sorted(s["tools"]), [
            "git", "kaizen-go-tables", "kaizen-python-tables", "kaizen-source-adapter", "kaizen-typescript-tables",
            "python", "tree-sitter", "tree-sitter-go", "tree-sitter-python", "tree-sitter-typescript",
        ])
        self.assertEqual(s["tools"]["kaizen-source-adapter"], source_adapter.SOURCE_ADAPTER_VERSION)
        self.assertEqual(s["tools"]["kaizen-go-tables"], lang_go.GO.version)

    def test_unavailable_signal_is_written_as_its_reason(self):
        written = json.loads(measure.dumps(self.measure()))
        self.assertEqual(written["repo"]["ci_median_ms"], {"unavailable": "no CI runs file given"})

    def test_adapter_omitting_a_declared_signal_fails_loudly(self):
        class Lazy(source_adapter.SourceAdapter):
            def measure(self, root, history):
                result = super().measure(root, history)
                del result.repo["clones"]
                return result

        with self.assertRaisesRegex(ValueError, r"omitted repo signals \['clones'\]"):
            measure.measure(self.root, "HEAD", None, (Lazy(lang_go.GO),))


class Rendering(unittest.TestCase):
    def signals(self, commit, hotspot, tools=(('git', '2'),)):
        pkg = {s: 0 for s in render.PACKAGE_SIGNALS} | {"hotspot": hotspot, "hotspot_rank": 1}
        repo = {s: 0 for s in render.REPO_SIGNALS} | {"hotspot": hotspot, "ci_median_ms": None}
        return {"commit": commit, "measured_at": "t", "window_days": 90, "tools": dict(tools),
                "packages": {"p/q": pkg}, "repo": repo}

    def test_report_shows_trend_against_previous_run_and_after_values(self):
        page = render.render_report(
            self.signals("a" * 40, 10), self.signals("b" * 40, 12), self.signals("c" * 40, 4),
            render.Pick("p/q", "P2 Duplicated knowledge", "hotspot", "<script>x</script>"),
        )
        self.assertIn('10 <span class="better">(-2)</span>', page)
        self.assertIn('4 <span class="better">(-6)</span>', page)
        self.assertIn("&lt;script&gt;", page)
        self.assertNotIn("<script>", page)

    def test_delta_exits_nonzero_unless_the_signal_dropped(self):
        with tempfile.TemporaryDirectory() as d:
            paths = {}
            for name, value in {"before": 10, "same": 10, "less": 4}.items():
                paths[name] = os.path.join(d, name + ".json")
                with open(paths[name], "w") as f:
                    json.dump(self.signals(name, value), f)
            argv = ["delta", "--package", "p/q", "--signal", "hotspot", "--before", paths["before"]]
            with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()):
                dropped = kaizen_signals.main([*argv, "--after", paths["less"]])
                held = kaizen_signals.main([*argv, "--after", paths["same"]])
        self.assertEqual((dropped, held), (0, 1))
        self.assertIn("Signals:   p/q hotspot: 10 → 4", out.getvalue())

    def test_regressions_list_other_signals_that_got_worse(self):
        before, worse = self.signals("a" * 40, 10), self.signals("b" * 40, 10)
        worse["packages"]["p/q"]["clones"] = 3
        self.assertEqual(render.regressions(before, worse, "p/q", "hotspot"), ["clones: 0 → 3"])

    def test_delta_refuses_to_compare_runs_with_different_tool_versions(self):
        with tempfile.TemporaryDirectory() as d:
            for name, tools in {"before": (("git", "2"),), "after": (("git", "3"),)}.items():
                with open(os.path.join(d, name), "w") as f:
                    json.dump(self.signals(name, 10 if name == "before" else 4, tools), f)
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()) as err:
                code = kaizen_signals.main(["delta", "--package", "p/q", "--signal", "hotspot",
                                            "--before", os.path.join(d, "before"), "--after", os.path.join(d, "after")])
        self.assertEqual(code, 1)
        self.assertIn("new baseline", err.getvalue())

    def test_report_drops_the_trend_when_the_previous_run_used_other_tools(self):
        page = render.render_report(
            self.signals("a" * 40, 10), self.signals("b" * 40, 12, (("git", "1"),)), None, None)
        self.assertIn("new baseline", page)
        self.assertNotIn('class="better"', page)

    def test_unavailable_values_render_as_a_dash(self):
        current = self.signals("a" * 40, 10)
        current["repo"]["ci_median_ms"] = {"unavailable": "no CI runs file given"}
        self.assertIn("<td>–</td>", render.render_report(current, None, None, None))
