import contextlib
import dataclasses
import io
import json
import math
import os
import subprocess
import sys
import tempfile
import unittest
import unittest.mock

import kaizen_signals
import lang_go
import lang_python
import lang_typescript
import measure
import mutation
import render
import source_adapter
import syntax
import file_signals
import gate
import graphs
import modules
from adapter import Commit, Unavailable
from file_signals import ViolationThresholds
from source_adapter import SourceAdapter

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


class RepoFixture(unittest.TestCase):
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



class MeasureRepo(RepoFixture):
    def measure(self):
        return measure.measure(self.root, "HEAD", None)

    def test_hotspot_is_decayed_churn_times_complexity_and_ranks_packages(self):
        s = self.measure()
        a = s["packages"]["a"]
        a_lines = len((BRANCHY + "\nfunc unusedHelper() {}\n").splitlines())
        decayed = math.log1p(a_lines) + math.log1p(2)  # commits made now: no decay
        self.assertEqual((a["churn"], a["complexity"], a["hotspot_rank"]), (2, 8, 1))
        self.assertAlmostEqual(a["hotspot"], decayed * 8, places=2)
        self.assertAlmostEqual(a["hotspot_per_loc"], decayed * 8 / a_lines, places=3)
        self.assertAlmostEqual(s["repo"]["hotspot"], decayed * 8, places=2)

    def test_relative_churn_is_changed_lines_over_current_lines(self):
        c = self.measure()["files"]["c/c.go"]
        self.assertEqual((c["loc"], c["relative_churn"]), (3, round((3 + 2) / 3, 3)))  # add 3 lines, then replace 1

    def test_per_loc_signals_do_not_rank_a_file_for_being_large(self):
        a = self.measure()["files"]["a/a.go"]
        self.assertEqual(a["complexity_per_loc"], round(a["complexity"] / a["loc"], 3))
        self.assertLess(a["hotspot_per_loc"], a["hotspot"])

    def test_older_changes_weigh_exp_minus_alpha_age_less(self):
        source = {"x.go": parse(lang_go.GO, "package x\n")}
        commits = [Commit(None, {"x.go": 0}, {"x.go": 9}, 0.0), Commit(None, {"x.go": 0}, {"x.go": 9}, 2.0)]
        score = file_signals.decayed_changes(commits, source)["x.go"]
        self.assertAlmostEqual(score, math.log(10) * (1 + math.exp(-file_signals.DECAY_PER_YEAR * 2)))

    def test_module_edges_weigh_imports_and_flag_cochange_without_one(self):
        edges = {(e["a"], e["b"]): e for e in self.measure()["module_edges"]}
        self.assertEqual(edges["a", "b"]["imports"], 1)
        self.assertEqual((edges["a", "b"]["cochange"], edges["a", "b"]["hidden"]), (0, False))
        self.assertEqual((edges["a", "c"]["imports"], edges["a", "c"]["cochange"], edges["a", "c"]["hidden"]), (0, 2, True))

    def test_modularity_violations_rank_unimported_cochange_with_support_confidence_and_card(self):
        adapters = (SourceAdapter(lang_go.GO, violation_thresholds=ViolationThresholds(min_support=2, min_confidence=0.5)),)
        found = measure.measure(self.root, "HEAD", None, adapters)["modularity_violations"]
        self.assertEqual(found, {"go": [{"a": "a/a.go", "b": "c/c.go", "support": 2, "confidence": 1.0, "last_card": None}]})

    def test_modularity_violations_ignore_test_files(self):
        for n in (1, 2):
            self.write("a/a_test.go", f"package a\n\nfunc TestA{n}() {{}}\n")
            self.write("c/c.go", f"package c\n\nfunc C() {{ _ = {n + 5} }}\n")
            self.commit(f"t{n}")
        adapters = (SourceAdapter(lang_go.GO, violation_thresholds=ViolationThresholds(min_support=2, min_confidence=0.1)),)
        found = measure.measure(self.root, "HEAD", None, adapters)["modularity_violations"]["go"]
        self.assertEqual({(v["a"], v["b"]) for v in found}, {("a/a.go", "c/c.go")})

    def test_modularity_violations_need_the_default_support_of_five(self):
        self.assertEqual(self.measure()["modularity_violations"], {"go": [], "python": [], "typescript": []})

    def test_modularity_violations_are_not_measured_when_no_import_resolves(self):
        self.write("a/a.go", "package a\n\nimport \"example.com/elsewhere/z\"\n\nvar _ = z.Z\n")
        self.write("b/b.go", "package b\n\nimport \"example.com/elsewhere/z\"\n\nvar _ = z.Z\n")
        self.commit("three")
        found = self.measure()["modularity_violations"]["go"]
        self.assertEqual(found, Unavailable("no go import resolved: dependencies unknown"))

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

    def test_file_pairs_cochanging_without_an_import_are_hidden_and_listed(self):
        s = self.measure()
        self.assertEqual(s["cochange_pairs"], [{"a": "a/a.go", "b": "c/c.go", "count": 2, "hidden": True}])
        self.assertEqual(s["files"]["c/c.go"]["hidden_coupling_ratio"], 1.0)
        self.assertEqual(s["files"]["b/b.go"]["cochange_pairs"], 0)

    def test_file_pairs_across_importing_packages_are_not_hidden(self):
        self.write("a/a.go", BRANCHY + "\nfunc unusedHelper() { _ = 2 }\n")
        self.write("b/b.go", 'package b\n\nimport "example.com/m/a"\n\nvar _ = a.Branchy // touched\n')
        self.commit("three")
        s = self.measure()
        self.assertEqual(s["files"]["b/b.go"]["cochange_pairs"], 1)
        self.assertEqual(s["files"]["b/b.go"]["hidden_pairs"], 0)

    def test_bulk_commits_by_file_count_make_no_file_pairs(self):
        for i in range(file_signals.MAX_FILES_PER_COMMIT + 1):
            self.write(f"bulk/f{i}.go", "package bulk\n")
        self.commit("bulk one")
        for i in range(file_signals.MAX_FILES_PER_COMMIT + 1):
            self.write(f"bulk/f{i}.go", "package bulk\n\nvar X = 1\n")
        self.commit("bulk two")
        self.assertEqual(self.measure()["files"]["bulk/f0.go"]["cochange_pairs"], 0)

    def test_files_and_functions_are_ranked_by_hotspot(self):
        s = self.measure()
        self.assertEqual(
            s["files"]["a/a.go"]["hotspot_rank"], 1)
        self.assertEqual(
            s["hotspot_functions"][0],
            {"file": "a/a.go", "name": "Branchy", "complexity": 8, "hotspot": s["files"]["a/a.go"]["hotspot"]})

    def test_cards_per_file_count_distinct_bead_trailers_and_rework_rate_counts_deleting_commits(self):
        self.write("c/c.go", "package c\n\nfunc C() { _ = 2 }\n")
        self.commit("three\n\nBead: card-1")
        self.write("c/c.go", "package c\n\nfunc C() { _ = 2 }\nfunc D() {}\n")
        self.commit("four\n\nBead: card-1")
        self.write("c/c.go", "package c\n\nfunc C() { _ = 2 }\nfunc D() {}\n\nfunc E() {}\n")
        self.commit("five\n\nBead: card-2")
        c = self.measure()["files"]["c/c.go"]
        # one: adds; two and three replace a line; four and five only add
        self.assertEqual((c["churn"], c["cards"], c["rework_rate"]), (5, 2, 0.4))

    def test_lcom4_counts_groups_of_files_that_never_reference_each_other(self):
        self.write("g/x.go", "package g\n\nfunc X() { Y() }\n")
        self.write("g/y.go", "package g\n\nfunc Y() {}\n")
        self.write("g/z.go", "package g\n\nfunc Z() {}\n")
        self.commit("group")
        packages = self.measure()["packages"]
        self.assertEqual((packages["g"]["lcom4"], packages["a"]["lcom4"]), (2, 1))

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

    def test_signals_record_schema_6_and_every_tool_version(self):
        s = self.measure()
        self.assertEqual(s["schema"], 6)
        self.assertEqual(sorted(s["tools"]), [
            "git", "gopls", "kaizen-go-tables", "kaizen-python-tables", "kaizen-source-adapter", "kaizen-typescript-tables",
            "pyright-langserver", "python", "tree-sitter", "tree-sitter-go", "tree-sitter-python", "tree-sitter-typescript",
            "typescript-language-server",
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


FAKE_LSP_SERVER = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fake_lsp_server.py")
GO_SOURCE_WITH_SERVER_CASES = BRANCHY + """
func unusedHelper() {}

func sharedName() {}

type T struct{}

func (T) sharedName() {}

func ByReflection(v any) {}
"""


class LanguageServerSignals(RepoFixture):
    """Signals read from a language server, here a scripted fake (fake_lsp_server.py)."""

    def setUp(self):
        super().setUp()
        self.write("a/a.go", GO_SOURCE_WITH_SERVER_CASES)
        self.commit("server cases")
        self.script_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.script_dir.cleanup)

    def at(self, path, name):
        with open(os.path.join(self.root, path), "rb") as f:
            declaration = next(d for d in syntax.parse_source(lang_go.GO, path, f.read()).declarations if d.name == name)
        return f"{path}:{declaration.line}:{declaration.character}"

    def measure_with(self, script, timeout=5.0, command=None):
        script_path = os.path.join(self.script_dir.name, "script.json")
        with open(script_path, "w") as f:
            json.dump(script, f)
        spec = dataclasses.replace(lang_go.GO, lsp_command=command or (sys.executable, FAKE_LSP_SERVER, script_path))
        return measure.measure(self.root, "HEAD", None, (source_adapter.SourceAdapter(spec, timeout),))

    def served(self):
        branchy, helper, by_reflection, c = (self.at("a/a.go", "Branchy"), self.at("a/a.go", "unusedHelper"),
                                             self.at("a/a.go", "ByReflection"), self.at("c/c.go", "C"))
        return {
            "references": {branchy: 1, by_reflection: 1, self.at("a/a.go", "T"): 1, c: 1},
            "calls": {branchy: [helper], helper: [c, "/outside/lib.go:0:0"], by_reflection: [], c: [branchy]},
            "hover": {branchy: "func Branchy(a, b bool) int", by_reflection: "func ByReflection(v any)", c: "func C()"},
        }

    def test_dead_symbols_are_declarations_the_server_finds_no_reference_to(self):
        a = self.measure_with(self.served())["packages"]["a"]
        # sharedName's name is also a method's, which a name count takes for a use; ByReflection is used only by reflection.
        self.assertEqual((a["dead_symbols"], a["dead_symbol_names"]), (2, ["sharedName", "unusedHelper"]))

    def test_call_depth_is_the_longest_call_chain_from_an_entry_point_within_the_repo(self):
        s = self.measure_with(self.served())
        # Branchy -> unusedHelper -> C -> Branchy (a cycle, cut where it closes); the call outside the repo does not count.
        self.assertEqual((s["packages"]["a"]["call_depth"], s["packages"]["c"]["call_depth"], s["repo"]["call_depth"]), (3, 1, 3))

    def test_typed_boundary_ratio_is_the_share_of_entry_point_functions_hovering_without_any(self):
        s = self.measure_with(self.served())
        self.assertEqual((s["packages"]["a"]["typed_boundary_ratio"], s["packages"]["c"]["typed_boundary_ratio"]), (0.5, 1.0))
        self.assertEqual(s["repo"]["typed_boundary_ratio"], 0.667)

    def test_server_version_is_recorded_as_a_tool(self):
        self.assertEqual(self.measure_with(self.served())["tools"][sys.executable], "fake-lsp 1.2.3")

    def test_server_that_cannot_start_makes_every_server_signal_unavailable_with_the_reason(self):
        s = self.measure_with(self.served(), command=("no-such-language-server",))
        reason = Unavailable("no-such-language-server: cannot start no-such-language-server: No such file or directory")
        for signals in (s["repo"], s["packages"]["a"]):
            self.assertEqual([signals[k] for k in ("dead_symbols", "call_depth", "typed_boundary_ratio")], [reason] * 3)
        self.assertEqual(s["tools"]["no-such-language-server"], "unavailable")

    def test_server_that_does_not_answer_in_time_is_unavailable_not_zero(self):
        s = self.measure_with({**self.served(), "mode": "silent"}, timeout=0.3)
        self.assertRegex(s["repo"]["dead_symbols"].reason, r"did not answer textDocument/references within 0\.3s")

    def test_server_that_exits_is_unavailable_not_zero(self):
        s = self.measure_with({**self.served(), "mode": "exit"})
        self.assertRegex(s["repo"]["call_depth"].reason, r"exited during textDocument/references")

    def test_edit_moves_server_signals_only_by_the_edit(self):
        before = self.measure_with(self.served())
        self.write("c/c.go", "package c\n\nfunc C() { _ = 2 }\n")
        self.commit("edit")
        after = self.measure_with(self.served())
        for key in ("dead_symbols", "call_depth", "typed_boundary_ratio"):
            self.assertEqual(before["repo"][key], after["repo"][key])


class GateAgainstHead(RepoFixture):
    def gate(self, limits=gate.Thresholds(), accepted=frozenset()):
        return gate.run_gate(self.root, "HEAD", limits, set(accepted))

    def test_unchanged_tree_has_no_findings(self):
        self.assertEqual(self.gate(), ([], []))

    def test_complexity_growth_in_a_changed_file_fails(self):
        self.write("c/c.go", "package c\n\nfunc C(a, b bool) int {\n\tif a && b {\n\t\treturn 1\n\t}\n\treturn 0\n}\n")
        report, findings = self.gate()
        self.assertEqual([f.key for f in findings], ["complexity:c/c.go"])
        self.assertIn("c/c.go: complexity 0 → 2, hotspot rank 3 → 2", report)

    def test_threshold_allows_growth_up_to_the_limit(self):
        self.write("c/c.go", "package c\n\nfunc C(a bool) {\n\tif a {\n\t}\n}\n")
        self.assertEqual(self.gate(gate.Thresholds(max_complexity_increase=1))[1], [])

    def test_baseline_accepts_a_known_finding(self):
        self.write("c/c.go", "package c\n\nfunc C(a bool) {\n\tif a {\n\t}\n}\n")
        self.assertEqual(self.gate(accepted={"complexity:c/c.go"})[1], [])

    def test_new_import_cycle_fails(self):
        self.write("a/a2.go", 'package a\n\nimport "example.com/m/b"\n\nvar _ = b.X\n')
        findings = self.gate()[1]
        self.assertIn("cycle:a", [f.key for f in findings])

    def test_new_clone_fails_in_a_new_untracked_file(self):
        self.write("a/c1.go", f"package a\n\nfunc One() {{\n{CLONE_BODY}}}\n")
        self.write("c/c1.go", f"package c\n\nfunc Two() {{\n{CLONE_BODY}}}\n")
        self.assertEqual({f.key for f in self.gate()[1]}, {"clones:a", "clones:c"})

    def test_hotspot_rank_rise_fails_only_with_a_configured_limit(self):
        self.write("c/c.go", "package c\n\n" + BRANCHY.split("\n", 2)[2].replace("Branchy", "C") * 3)
        self.assertEqual([f.key for f in self.gate()[1] if f.key.startswith("hotspot")], [])
        keys = [f.key for f in self.gate(gate.Thresholds(max_hotspot_rank_rise=0))[1]]
        self.assertIn("hotspot:c/c.go", keys)

    def test_thresholds_file_rejects_unknown_keys(self):
        self.write("t.json", '{"max_complexity_incress": 3}')
        with self.assertRaises(TypeError):
            gate.load_thresholds(os.path.join(self.root, "t.json"))

    def test_cli_exits_non_zero_on_a_finding(self):
        self.write("c/c.go", "package c\n\nfunc C(a bool) {\n\tif a {\n\t}\n}\n")
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(kaizen_signals.main(["gate", "--root", self.root, "--base", "HEAD"]), 1)


class ModuleFacts(unittest.TestCase):
    def test_purpose_is_the_first_doc_line_in_each_language(self):
        self.assertEqual(parse(lang_go.GO, "// Package a does things.\n// More.\npackage a\n").purpose, "Package a does things.")
        self.assertEqual(parse(lang_python.PYTHON, '# /// script\n"""Does things.\n\nMore."""\nX = 1\n').purpose, "Does things.")
        self.assertEqual(parse(lang_typescript.TYPESCRIPT, "/**\n * Does things.\n */\nexport const x = 1;\n").purpose, "Does things.")
        self.assertEqual(parse(lang_go.GO, "package a\n\n// F is not the package doc.\nfunc F() {}\n").purpose, "")

    def test_entry_points_are_what_each_language_exports(self):
        self.assertEqual(parse(lang_go.GO, "package a\nfunc Pub() {}\nfunc priv() {}\n").entry_points, ["Pub"])
        self.assertEqual(parse(lang_python.PYTHON, "def pub(): pass\ndef _priv(): pass\n").entry_points, ["pub"])
        self.assertEqual(
            parse(lang_typescript.TYPESCRIPT, "export function pub() {}\nfunction priv() {}\n").entry_points, ["pub"])

    def test_symbol_refs_hold_the_names_a_declaration_uses_but_not_its_own(self):
        parsed = parse(lang_go.GO, "package a\nfunc A() { B(); A() }\nfunc B() {}\n")
        self.assertEqual(parsed.symbol_refs, {"A": frozenset({"B"}), "B": frozenset()})

    def test_symbol_edges_link_symbols_across_the_files_of_a_package(self):
        files = {
            "a/x.go": parse(lang_go.GO, "package a\nfunc X() { Y() }\n", "a/x.go"),
            "a/y.go": parse(lang_go.GO, "package a\nfunc Y() { fmt.Println() }\n", "a/y.go"),
        }
        self.assertEqual(file_signals.symbol_edges(files), [["X", "Y"]])


class ModuleMap(unittest.TestCase):
    def packages(self, **overrides):
        base = {"purpose": "p", "entry_points": ["E"], "imports": {"b": 2}, "lcom4": 1,
                "hotspot_rank": 1, "hidden_coupling_ratio": 0.0}
        return {"a": base | overrides, "b": base | {"imports": {}, "hotspot_rank": 2}}

    def signals(self, commit, **overrides):
        return {"commit": commit, "packages": self.packages(**overrides)}

    def test_entries_list_who_imports_whom_in_both_directions(self):
        entries = modules.module_entries(self.packages(), "c1")
        self.assertEqual((entries["a"]["imports"], entries["a"]["imported_by"]), (["b"], []))
        self.assertEqual((entries["b"]["imports"], entries["b"]["imported_by"]), ([], ["a"]))

    def test_entry_points_are_capped_with_the_total_kept(self):
        names = [f"E{i:02d}" for i in range(modules.MAX_ENTRY_POINTS + 3)]
        entry = modules.module_entries(self.packages(entry_points=names), "c1")["a"]
        self.assertEqual((len(entry["entry_points"]), entry["entry_point_count"]), (modules.MAX_ENTRY_POINTS, len(names)))

    def test_unchanged_modules_keep_their_committed_entry_and_revision(self):
        first = modules.update_map(self.signals("c1"), None)
        later = modules.update_map(self.signals("c2", hotspot_rank=3, hidden_coupling_ratio=0.1), first)
        self.assertEqual(later, first)

    def test_a_tree_change_or_a_large_history_shift_refreshes_that_module_only(self):
        first = modules.update_map(self.signals("c1"), None)
        for change in ({"entry_points": ["E", "F"]}, {"hotspot_rank": 1 + modules.RANK_SHIFT},
                       {"hidden_coupling_ratio": modules.HIDDEN_RATIO_SHIFT}):
            later = modules.update_map(self.signals("c2", **change), first)["modules"]
            self.assertEqual(later["a"]["measured_at_commit"], "c2", change)
            self.assertEqual(later["b"], first["modules"]["b"], change)

    def test_modules_gone_from_the_tree_leave_the_map(self):
        first = modules.update_map(self.signals("c1"), None)
        trimmed = {"commit": "c2", "packages": {"b": self.packages()["b"]}}
        self.assertEqual(sorted(modules.update_map(trimmed, first)["modules"]), ["b"])

    def test_check_names_each_way_the_map_disagrees_with_the_tree(self):
        with tempfile.TemporaryDirectory() as root:
            for rel, text in (("go.mod", "module example.com/m\n"), ("a/a.go", "// Package a is A.\npackage a\n\nfunc A() {}\n")):
                os.makedirs(os.path.dirname(os.path.join(root, rel)), exist_ok=True)
                with open(os.path.join(root, rel), "w") as f:
                    f.write(text)
            fresh = modules.tree_entries(root)
            self.assertEqual(modules.check_map(root, {"schema": modules.SCHEMA, "modules": fresh}), [])
            stale = {"schema": modules.SCHEMA, "modules": {"a": fresh["a"] | {"entry_points": []}, "gone": fresh["a"]}}
            self.assertEqual(modules.check_map(root, stale), [
                "a: entry_points is [], tree has ['A']",
                "gone: in the map but not in the tree",
            ])
            self.assertEqual(modules.check_map(root, {"schema": 0, "modules": {}}), ["map schema is 0, want 1"])

    def test_check_command_exits_nonzero_on_drift(self):
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, "a"))
            with open(os.path.join(root, "a/a.py"), "w") as f:
                f.write('"""A."""\n')
            path = os.path.join(root, "modules.json")
            with open(path, "w") as f:
                json.dump({"schema": modules.SCHEMA, "modules": {}}, f)
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(kaizen_signals.main(["modules-check", "--root", root, "--map", path]), 1)


class GraphViews(unittest.TestCase):
    def test_hidden_coupling_is_a_dashed_edge_and_imports_are_solid(self):
        edges = [
            {"a": "x", "b": "y", "imports": 3, "cochange": 0, "hidden": False},
            {"a": "x", "b": "z", "imports": 0, "cochange": 4, "hidden": True},
            {"a": "y", "b": "z", "imports": 0, "cochange": 9, "hidden": False},
        ]
        svg = graphs.dependency_graph(edges, ["x", "y", "z"])
        self.assertEqual(svg.count("<line"), 2)
        self.assertEqual(svg.count("stroke-dasharray"), 1)
        self.assertIn("x – z: 0 imports, 4 co-changes", svg)

    def test_cohesion_graph_colours_each_connected_component_apart(self):
        svg, drawn = graphs.cohesion_graph([["A", "B"], ["C", "D"]])
        self.assertEqual(drawn, 4)
        self.assertEqual(len({c for c in graphs.COMPONENT_COLOURS if f'fill="{c}"' in svg}), 2)

    def test_cohesion_graph_keeps_the_most_connected_symbols_past_the_cap(self):
        edges = [["hub", f"s{i:02d}"] for i in range(graphs.MAX_GRAPH_NODES + 5)]
        svg, drawn = graphs.cohesion_graph(edges)
        self.assertEqual(drawn, graphs.MAX_GRAPH_NODES)
        self.assertIn(">hub<", svg)

    def test_the_same_signals_draw_the_same_picture(self):
        edges = [["A", "B"], ["B", "C"]]
        self.assertEqual(graphs.cohesion_graph(edges), graphs.cohesion_graph(edges))

    def test_report_lists_violations_and_says_not_measured_for_unresolved_languages(self):
        signals = Rendering().signals("a" * 40, 1)
        signals["modularity_violations"] = {
            "go": [{"a": "x/a.go", "b": "y/b.go", "support": 6, "confidence": 0.5, "last_card": "c-1"}],
            "python": {"unavailable": "no python import resolved"},
        }
        page = render.render_report(signals, None, None, None)
        self.assertIn("<td>x/a.go</td><td>y/b.go</td><td>6</td><td>50%</td><td>c-1</td>", page)
        self.assertIn("Not measured: no python import resolved.", page)
        self.assertNotIn("Hidden coupling</th>", page)

    def test_report_carries_both_graph_views(self):
        signals = Rendering().signals("a" * 40, 1)
        signals["packages"]["p/q"] |= {"symbol_edges": [["A", "B"]], "lcom4": 2}
        signals["module_edges"] = [{"a": "p/q", "b": "p/r", "imports": 0, "cochange": 3, "hidden": True}]
        page = render.render_report(signals, None, None, None)
        self.assertIn("Module dependency graph", page)
        self.assertIn("p/q (LCOM4 2, 2 connected symbols drawn)", page)
        self.assertIn("<td>3</td>", page)


class Rendering(unittest.TestCase):
    def signals(self, commit, hotspot, tools=(('git', '2'),)):
        pkg = {s: 0 for s in render.PACKAGE_SIGNALS} | {"hotspot": hotspot, "hotspot_rank": 1, "symbol_edges": []}
        repo = {s: 0 for s in render.REPO_SIGNALS} | {"hotspot": hotspot, "ci_median_ms": None}
        return {"commit": commit, "measured_at": "t", "window_days": 90, "tools": dict(tools),
                "packages": {"p/q": pkg}, "repo": repo, "module_edges": [], "modularity_violations": {}}

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


class MutationScore(unittest.TestCase):
    GREMLINS = {"files": [{"file_name": "a.go", "mutations": [
        {"status": "KILLED"}, {"status": "KILLED"}, {"status": "LIVED"}, {"status": "NOT COVERED"},
        {"status": "TIMED OUT"}, {"status": "NOT VIABLE"}]}]}

    def fake_run(self, report_name=None, report=None, stdout="", timeout_on=None):
        def run(argv, cwd, timeout):
            if argv[1:2] == ["version"] or argv[-1] == "--version":
                return "v0.5.1\n"
            if timeout_on:
                raise subprocess.TimeoutExpired(argv, timeout)
            if report_name:
                out = argv[argv.index("--output") + 1] if "--output" in argv else argv[argv.index("--jsonReporter.fileName") + 1]
                with open(out, "w") as f:
                    json.dump(report, f)
            return stdout
        return run

    def test_score_counts_survivors_and_uncovered_mutants_against_the_tests(self):
        got = mutation.mutation_signals(".", "go", ("pkg",), run=self.fake_run("g", self.GREMLINS))
        self.assertEqual((got["killed"], got["survived"], got["timed_out"]), (2, 2, 1))
        self.assertEqual(got["mutation_score"], 3 / 5)
        self.assertEqual(got["tool_version"], "v0.5.1")

    def test_hitting_the_cap_makes_the_score_unavailable_not_zero(self):
        got = mutation.mutation_signals(".", "go", ("pkg",), cap_seconds=30, run=self.fake_run(timeout_on=True))
        self.assertEqual(got["mutation_score"], Unavailable("gremlins hit the 30s cap"))
        self.assertEqual(got["killed"], Unavailable("gremlins hit the 30s cap"))

    def test_a_missing_tool_makes_the_score_unavailable(self):
        def run(argv, cwd, timeout):
            raise FileNotFoundError(argv[0])
        got = mutation.mutation_signals(".", "python", ("a.py",), run=run)
        self.assertEqual(got["mutation_score"], Unavailable("mutmut is not installed"))

    def test_a_scope_without_mutants_is_unavailable_not_perfect(self):
        got = mutation.mutation_signals(".", "go", ("pkg",), run=self.fake_run("g", {"files": []}))
        self.assertEqual(got["mutation_score"], Unavailable("the tool found no mutants in the scope"))

    def test_mutmut_progress_line_gives_the_counts(self):
        out = "running\n⠏ 12/12  🎉 9 ⏰ 1  🤔 1  🙁 1  🔇 0\n"
        got = mutation.mutation_signals(".", "python", ("a.py",), run=self.fake_run(stdout=out))
        self.assertEqual((got["killed"], got["survived"], got["timed_out"]), (9, 2, 1))

    def test_stryker_report_gives_the_counts(self):
        report = {"files": {"a.ts": {"mutants": [
            {"status": "Killed"}, {"status": "Survived"}, {"status": "NoCoverage"}, {"status": "Timeout"},
            {"status": "CompileError"}]}}}
        got = mutation.mutation_signals(".", "typescript", ("a.ts",), run=self.fake_run("s", report))
        self.assertEqual((got["killed"], got["survived"], got["timed_out"]), (1, 2, 1))

    def test_cli_writes_unavailable_signals_as_reasons(self):
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "m.json")
            env = {**os.environ, "PATH": d}  # no gremlins on PATH
            with unittest.mock.patch.dict(os.environ, env, clear=True):
                kaizen_signals.main(["mutation", "--root", d, "--language", "go", "--out", out, "pkg"])
            with open(out) as f:
                written = json.load(f)
        self.assertEqual(written["mutation_score"], {"unavailable": "gremlins is not installed"})
