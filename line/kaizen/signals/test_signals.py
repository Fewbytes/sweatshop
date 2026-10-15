import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest

import golang
import kaizen_signals
import measure
import render

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


class GoSourceFacts(unittest.TestCase):
    def test_scan_drops_comments_and_empties_string_contents(self):
        code, blanked = golang.scan('x := "if {" // if\n/* for */ y := `a\nb`\n')
        self.assertNotIn("//", code)
        self.assertNotIn("for", code)
        self.assertIn('"if {"', code)
        self.assertNotIn("if", blanked)
        self.assertEqual(code.count("\n"), blanked.count("\n"))

    def test_cognitive_complexity_counts_branches_nesting_else_and_operators(self):
        _, blanked = golang.scan(BRANCHY)
        self.assertEqual(
            [(f.name, f.complexity) for f in golang.function_complexities(blanked)],
            [("Branchy", 8)],
        )

    def test_imports_read_single_and_block_forms(self):
        code, _ = golang.scan('package a\nimport "os"\nimport (\n\t"fmt"\n\tx "m/b"\n)\n')
        self.assertEqual(sorted(golang.imports(code)), ["fmt", "m/b", "os"])

    def test_declared_symbols_skip_methods_and_entry_points(self):
        _, blanked = golang.scan(
            "package a\nfunc Used() {}\nfunc (t T) Method() {}\nfunc main() {}\n"
            "type T struct{}\nconst (\n\tOne = 1\n\ttwo = 2\n)\nvar v int\nfunc TestX(t *testing.T) {}\n"
        )
        self.assertEqual(sorted(golang.declared_symbols(blanked)), ["One", "T", "Used", "two", "v"])


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


class Rendering(unittest.TestCase):
    def signals(self, commit, hotspot):
        pkg = {s: 0 for s in render.PACKAGE_SIGNALS} | {"hotspot": hotspot, "hotspot_rank": 1}
        repo = {s: 0 for s in render.REPO_SIGNALS} | {"hotspot": hotspot, "ci_median_ms": None}
        return {"commit": commit, "measured_at": "t", "window_days": 90, "packages": {"p/q": pkg}, "repo": repo}

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
