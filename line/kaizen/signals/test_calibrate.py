import os
import subprocess
import tempfile
import unittest

import calibrate
from calibrate import FixCase


def git(root, *args):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
    return subprocess.run(["git", "-C", root, *args], check=True, capture_output=True, text=True, env=env).stdout.strip()


class RankingScores(unittest.TestCase):
    def test_auc_is_one_when_positives_outrank_and_half_on_ties(self):
        self.assertEqual(calibrate.auc({"a": 3, "b": 2, "c": 1}, frozenset({"a"})), 1.0)
        self.assertEqual(calibrate.auc({"a": 1, "b": 1}, frozenset({"a"})), 0.5)
        self.assertEqual(calibrate.auc({"a": 1, "b": 2}, frozenset({"a"})), 0.0)

    def test_auc_is_undefined_without_both_kinds(self):
        self.assertIsNone(calibrate.auc({"a": 1}, frozenset({"a"})))

    def test_precision_counts_positives_among_the_top_k(self):
        scores = {"a": 5, "b": 4, "c": 3, "d": 1}
        self.assertEqual(calibrate.precision_at_positives(scores, frozenset({"a", "c"})), 0.5)

    def test_verdict_needs_enough_fixes_and_an_interval_clear_of_chance(self):
        self.assertEqual(calibrate.verdict([0.9] * 3), "inconclusive")
        self.assertEqual(calibrate.verdict([0.8, 0.9] * 5), "predictive")
        self.assertEqual(calibrate.verdict([0.1, 0.2] * 5), "anti-predictive")
        self.assertEqual(calibrate.verdict([0.1, 0.9] * 5), "noise")


class CalibrateOverFixes(unittest.TestCase):
    def test_signal_that_tracks_the_touched_files_is_predictive_and_a_constant_is_noise(self):
        files = {f"f{i}.go": {s: 0 for s in calibrate.SIGNALS} for i in range(4)}
        cases = []
        for n in range(12):
            touched = f"f{n % 4}.go"
            cases.append(FixCase(f"b{n}", f"base{n}", frozenset({touched, "new.go"})))
        def measure_at(base):
            n = int(base[len("base"):])
            return {p: {**f, "hotspot": 9 if p == f"f{n % 4}.go" else 1, "rework_rate": n % 2} for p, f in files.items()}
        result = calibrate.calibrate(cases, measure_at)
        self.assertEqual(result["fixes_used"], 12)
        self.assertEqual(result["signals"]["hotspot"]["verdict"], "predictive")
        self.assertEqual(result["signals"]["hotspot"]["precision_at_k"], 1.0)
        self.assertEqual(result["signals"]["hotspot"]["chance"], 0.25)
        self.assertEqual(result["signals"]["complexity"]["auc"], 0.5)
        self.assertEqual(result["signals"]["complexity"]["verdict"], "noise")

    def test_fix_touching_only_new_files_is_not_scored(self):
        result = calibrate.calibrate([FixCase("b", "base", frozenset({"new.go"}))],
                                     lambda base: {"a.go": {s: 1 for s in calibrate.SIGNALS}})
        self.assertEqual((result["fixes_used"], result["fixes_found"]), (0, 1))
        self.assertEqual(result["signals"]["hotspot"], {"fixes": 0, "verdict": "inconclusive"})

    def test_markdown_states_the_sample_and_every_signal(self):
        result = calibrate.calibrate([], lambda base: {})
        text = calibrate.render_markdown({**result, "skipped": {}})
        self.assertIn("0 of 0 bug-card fixes", text)
        self.assertIn("| hidden_pairs | 0 | n/a", text)


class FixCasesFromHistory(unittest.TestCase):
    def test_fix_case_is_the_parent_of_the_first_bead_commit_and_names_skipped_bug_cards(self):
        with tempfile.TemporaryDirectory() as root:
            git(root, "init", "-q", "--initial-branch=master")
            for name, text, msg in [("a.go", "1\n", "start"), ("a.go", "2\n", "fix\n\nBead: bug-1"), ("b.go", "x\n", "other\n\nBead: task-1")]:
                with open(os.path.join(root, name), "w") as f:
                    f.write(text)
                git(root, "add", "-A")
                git(root, "commit", "-q", "-m", msg)
            before_fix = git(root, "rev-parse", "HEAD~2")
            cards = [{"id": "bug-1", "issue_type": "bug"}, {"id": "bug-2", "issue_type": "bug"}, {"id": "task-1", "issue_type": "task"}]
            cases, skipped = calibrate.fix_cases(root, "HEAD", cards)
        self.assertEqual(cases, [FixCase("bug-1", before_fix, frozenset({"a.go"}))])
        self.assertEqual(skipped, {"bug-2": "no commit carries its Bead trailer"})


if __name__ == "__main__":
    unittest.main()
