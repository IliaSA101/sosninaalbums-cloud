"""runner_check_cases (design 004 §12): тот же текст шага, что в workflow, на синтетических ответах API заданий прогона."""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

wfparse = rtlib.load(os.path.join(rtlib.REL, "wfparse.py"), "wfparse")


def step(fname, job):
    wf = wfparse.parse(rtlib.read(os.path.join(rtlib.ROOT, ".github", "workflows", fname)))
    return [s for s in wf["jobs"][job]["steps"] if "gh api" in str(s.get("run", ""))][0]


def hosted(name, **kw):
    j = {"name": name, "status": "completed", "conclusion": "success", "runner_id": 1000000001, "runner_name": "GitHub Actions 1000000001",
         "runner_group_id": 0, "runner_group_name": "GitHub Actions", "labels": ["ubuntu-24.04"]}
    j.update(kw)
    return j


def good(extra=()):
    names = ["release-gate", "checks / check", "checks / build-image", "checks / stand-full", "release-publish"]
    me = hosted("release-attest", status="in_progress", conclusion=None)
    return [hosted(n) for n in names] + [me] + list(extra)


class RunnerCheck(rtlib.Case):
    def run_step(self, jobs=None, pages=None, fname="release.yml", job="release-attest", gh_fail=False):
        state = self.path("state")
        os.makedirs(state, exist_ok=True)
        pages = pages if pages is not None else [{"total_count": len(jobs), "jobs": jobs}]
        with open(os.path.join(state, "pages.json"), "w", encoding="utf-8") as f:
            f.write("\n".join(json.dumps(p) for p in pages))
        st = step(fname, job)
        script = self.path("step.sh")
        rtlib.write(script, st["run"])
        tmp = self.path("tmp")
        os.makedirs(tmp, exist_ok=True)
        env = rtlib.shim_env(state, RUNNER_TEMP=tmp, GITHUB_REPOSITORY="example/repo", GITHUB_RUN_ID="42", GITHUB_JOB=job,
                             GH_TOKEN="decoy-token", SA_REQUIRED_JOBS=st["env"]["SA_REQUIRED_JOBS"])
        if gh_fail:
            env["SHIM_GH_FAIL"] = "1"
        return rtlib.sh(["bash", "-e", script], env=env)

    def stops(self, want, **kw):
        rc, out = self.run_step(**kw)
        self.assertNotEqual(rc, 0, out)
        self.assertIn(want, out)

    def test_correct_answer_passes(self):
        rc, out = self.run_step(good())
        self.assertEqual(rc, 0, out)

    def test_foreign_group_with_right_label_stops(self):
        jobs = good()
        jobs[2] = hosted("checks / build-image", runner_group_id=7, runner_group_name="Default", labels=["ubuntu-24.04"])
        self.stops("runner group 7", jobs=jobs)

    def test_foreign_group_on_second_page_stops(self):
        first = {"total_count": 7, "jobs": good()[:3]}
        second = {"total_count": 7, "jobs": good()[3:] + [hosted("checks / check", runner_group_id=3, runner_group_name="Self")]}
        self.stops("runner group 3", pages=[first, second])

    def test_missing_required_name_stops(self):
        jobs = [j for j in good() if j["name"] != "checks / build-image"]
        self.stops("missing job with success: checks / build-image", jobs=jobs)

    def test_empty_answer_and_gh_error_stop(self):
        self.stops("empty answer", jobs=[])
        rc, _ = self.run_step(good(), gh_fail=True)
        self.assertNotEqual(rc, 0)

    def test_second_attempt_foreign_build_image_stops(self):
        # перезапуск упавших: build-image первой попытки остался на чужой группе, вторая попытка - на верной
        jobs = good([hosted("checks / build-image", runner_group_id=9, runner_group_name="Own", conclusion="failure")])
        self.stops("runner group 9", jobs=jobs)

    def test_job_without_runner(self):
        ok = good([hosted("checks / extra", runner_id=None, runner_name=None, runner_group_id=None, runner_group_name=None, conclusion="skipped")])
        self.assertEqual(self.run_step(ok)[0], 0)
        bad = good([hosted("checks / extra", runner_id=None, runner_name=None, runner_group_id=None, runner_group_name=None, conclusion="success")])
        self.stops("no runner", jobs=bad)
        queued = good([hosted("checks / extra", runner_id=None, runner_name=None, runner_group_id=None, runner_group_name=None, status="queued", conclusion=None)])
        self.stops("no runner", jobs=queued)

    def test_required_name_must_have_success(self):
        jobs = good()
        jobs[4] = hosted("release-publish", conclusion="failure")
        self.stops("missing job with success: release-publish", jobs=jobs)

    def test_ci_variant_needs_only_checks_names(self):
        jobs = [hosted("probe"), hosted("checks / build-image"), hosted("checks / stand-full"), hosted("runner-check", status="in_progress", conclusion=None)]
        rc, out = self.run_step(jobs, fname="ci.yml", job="runner-check")
        self.assertEqual(rc, 0, out)
        self.stops("missing job", jobs=jobs[:2] + jobs[3:], fname="ci.yml", job="runner-check")

    def test_same_text_in_release_and_ci(self):
        self.assertEqual(step("release.yml", "release-attest")["run"], step("ci.yml", "runner-check")["run"])
        self.assertTrue(step("release.yml", "release-attest")["run"].isascii())


if __name__ == "__main__":
    rtlib.main()
