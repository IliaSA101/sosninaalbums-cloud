"""release_gate_cases и сверка дерева (design 004 §12, 004-18): gate.sh и tree_check.sh на временном репозитории."""

import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

GATE = os.path.join(rtlib.REL, "gate.sh")
TREE = os.path.join(rtlib.REL, "tree_check.sh")
# Без пользовательской конфигурации: тест не зависит от настроек машины и ничего в них не пишет.
GENV = {
    "GIT_AUTHOR_NAME": "t",
    "GIT_AUTHOR_EMAIL": "t@example.org",
    "GIT_COMMITTER_NAME": "t",
    "GIT_COMMITTER_EMAIL": "t@example.org",
    "GIT_CONFIG_GLOBAL": os.devnull,
    "GIT_CONFIG_SYSTEM": os.devnull,
}


class Repo(rtlib.Case):
    def setUp(self):
        super().setUp()
        self.repo = self.path("repo")
        os.makedirs(self.repo)
        self.git("init", "-q", "-b", "main")
        self.commit("a")
        self.on_main = self.git("rev-parse", "HEAD").strip()
        self.git("update-ref", "refs/remotes/origin/main", self.on_main)
        self.git("checkout", "-q", "-b", "side")
        self.commit("b")
        self.off_main = self.git("rev-parse", "HEAD").strip()

    def git(self, *a):
        p = subprocess.run(["git", *a], cwd=self.repo, env={**os.environ, **GENV}, capture_output=True, text=True, check=True)
        return p.stdout

    def commit(self, name):
        with open(os.path.join(self.repo, name), "w", encoding="utf-8") as f:
            f.write(name)
        self.git("add", name)
        self.git("commit", "-q", "-m", name)

    def gate(self, ref, event="push", sha=None, tag=True):
        # Метка живёт в локальном репозитории, как после checkout с fetch-depth: 0 (gate.sh читает её объект).
        if tag and ref.startswith("refs/tags/"):
            self.git("tag", "-f", ref.removeprefix("refs/tags/"), sha if sha and len(sha) == 40 else self.on_main)
        env = {**GENV, "GITHUB_REF": ref, "GITHUB_EVENT_NAME": event, "GITHUB_SHA": sha or self.on_main}
        return rtlib.sh(["bash", GATE], cwd=self.repo, env=env)


class Gate(Repo):
    def test_main_tag_passes(self):
        for ref in ("refs/tags/v1.2.3", "refs/tags/v0.1.0", "refs/tags/v10.20.30"):
            rc, out = self.gate(ref)
            self.assertEqual(rc, 0, (ref, out))

    def test_bad_tag_names(self):
        refs = ("refs/tags/v1.2", "refs/tags/v1.2.3-rc1", "refs/tags/v01.2.3", "refs/heads/main", "refs/tags/1.2.3", "refs/tags/v1.2.3.x", "v1.2.3", "")
        for ref in refs:
            rc, out = self.gate(ref)
            self.assertEqual(rc, 1, (ref, out))

    def test_not_push_event(self):
        for ev in ("workflow_dispatch", "pull_request", ""):
            self.assertEqual(self.gate("refs/tags/v1.2.3", event=ev)[0], 1, ev)

    def test_commit_not_in_main(self):
        rc, out = self.gate("refs/tags/v1.2.3", sha=self.off_main)
        self.assertEqual(rc, 1)
        self.assertIn("не входит в main", out)

    def test_annotated_tag_is_refused(self):
        self.git("tag", "-a", "-m", "m", "v2.0.0", self.on_main)
        rc, out = self.gate("refs/tags/v2.0.0", tag=False)
        self.assertEqual(rc, 1)
        self.assertIn("не лёгкая", out)

    def test_tag_missing_locally_is_refused(self):
        rc, out = self.gate("refs/tags/v3.0.0", tag=False)
        self.assertEqual(rc, 1, out)

    def test_top_of_main_is_reported(self):
        rc, out = self.gate("refs/tags/v1.2.3")
        self.assertIn("верхний main = метка: да", out)
        self.git("checkout", "-q", "main")
        self.commit("c")
        self.git("update-ref", "refs/remotes/origin/main", self.git("rev-parse", "HEAD").strip())
        rc, out = self.gate("refs/tags/v1.2.4")  # метка на старом коммите main: не отказ, но видно
        self.assertEqual(rc, 0, out)
        self.assertIn("верхний main = метка: НЕТ", out)

    def test_tag_on_another_main_commit_than_github_sha(self):
        # метка указывает на один коммит main, а GITHUB_SHA - на другой (верхний): отказ
        self.git("checkout", "-q", "main")
        self.commit("c")
        tip = self.git("rev-parse", "HEAD").strip()
        self.git("update-ref", "refs/remotes/origin/main", tip)
        self.git("tag", "-f", "v1.2.5", self.on_main)
        rc, out = self.gate("refs/tags/v1.2.5", sha=tip, tag=False)
        self.assertEqual(rc, 1, out)
        self.assertIn("не на GITHUB_SHA", out)

    def test_no_test_hooks_in_gate(self):
        # переопределить main через окружение нельзя: обход «коммит из main» (аудит И6a)
        self.git("tag", "-f", "v1.2.3", self.off_main)
        rc, out = rtlib.sh(["bash", GATE], cwd=self.repo, env={**GENV, "GITHUB_REF": "refs/tags/v1.2.3", "GITHUB_EVENT_NAME": "push",
                                                               "GITHUB_SHA": self.off_main, "SA_GATE_MAIN_REF": "HEAD"})
        self.assertEqual(rc, 1, out)

    def test_bad_sha_and_missing_main(self):
        self.assertEqual(self.gate("refs/tags/v1.2.3", sha="abc")[0], 1)
        self.git("update-ref", "-d", "refs/remotes/origin/main")
        self.assertEqual(self.gate("refs/tags/v1.2.3")[0], 1)


class Tree(Repo):
    def test_equal_trees_pass_and_extra_commit_fails(self):
        self.git("checkout", "-q", "main")
        self.git("checkout", "-q", "-b", "ci/x")
        self.assertEqual(rtlib.sh(["bash", TREE, "main", "ci/x"], cwd=self.repo, env=GENV)[0], 0)
        self.git("checkout", "-q", "main")
        self.commit("c")
        # первый аргумент - помечаемый коммит (имя ссылки или sha)
        rc, out = rtlib.sh(["bash", TREE, self.git("rev-parse", "HEAD").strip(), "ci/x"], cwd=self.repo, env=GENV)
        self.assertEqual(rc, 1, out)
        self.assertIn("между зелёным ci/* и помечаемым коммитом есть правки", out)

    def test_missing_ref_and_bad_name(self):
        self.assertEqual(rtlib.sh(["bash", TREE, "main", "ci/none"], cwd=self.repo, env=GENV)[0], 1)
        self.assertEqual(rtlib.sh(["bash", TREE, "main", "--exec=x"], cwd=self.repo, env=GENV)[0], 1)


if __name__ == "__main__":
    rtlib.main()
