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


def rerun_answer():
    """Настоящий ответ API после `gh run rerun --failed` (прогон выпуска v0.1.0): задания попытки 1 и их копии в попытке 2."""
    with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "rerun-jobs.json"), encoding="utf-8") as f:
        jobs = json.load(f)["jobs"]
    for j in jobs:
        if j["name"] == "release-attest" and j["run_attempt"] == 2:  # на момент шага это задание ещё идёт
            j.update(status="in_progress", conclusion=None)
    return jobs


def find(jobs, name, attempt):
    return [j for j in jobs if j["name"] == name and j["run_attempt"] == attempt][0]


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

    def test_rerun_copies_from_real_answer_pass(self):
        # копии при перезапуске: тот же name и runner_id, runner_group_id = null, оригинал в попытке 1 на верной группе
        jobs = rerun_answer()
        copies = [j for j in jobs if j["runner_group_id"] is None]
        self.assertEqual(len(copies), 5)
        rc, out = self.run_step(jobs)
        self.assertEqual(rc, 0, out)

    def test_rerun_copy_with_swapped_runner_id_stops(self):
        jobs = rerun_answer()
        find(jobs, "checks / build-image", 2)["runner_id"] = 1000009999
        self.stops("checks / build-image: runner group None", jobs=jobs)

    def test_rerun_copy_with_runner_id_of_another_job_stops(self):
        # runner_id копии build-image совпал с runner_id оригинала ДРУГОГО задания (check): имя обязано совпадать тоже
        jobs = rerun_answer()
        find(jobs, "checks / build-image", 2)["runner_id"] = find(jobs, "checks / check", 1)["runner_id"]
        self.stops("checks / build-image: runner group None", jobs=jobs)

    def test_rerun_original_not_success_stops(self):
        # GitHub копирует только успешные задания: оригинал с failure копией не оправдывается
        jobs = rerun_answer()
        find(jobs, "checks / build-image", 1)["conclusion"] = "failure"
        self.stops("checks / build-image: runner group None", jobs=jobs)

    def test_rerun_copy_without_original_stops(self):
        jobs = [j for j in rerun_answer() if not (j["name"] == "release-publish" and j["run_attempt"] == 1)]
        self.stops("release-publish: runner group None", jobs=jobs)

    def test_rerun_original_with_foreign_group_stops(self):
        jobs = rerun_answer()
        find(jobs, "checks / build-image", 1).update(runner_group_id=7, runner_group_name="Default")
        rc, out = self.run_step(jobs)
        self.assertNotEqual(rc, 0, out)
        self.assertIn("checks / build-image: runner group 7", out)
        self.assertIn("checks / build-image: runner group None", out)  # копия без верного оригинала тоже отказ

    def test_rerun_copy_needs_an_earlier_attempt(self):
        # оригинал в той же попытке или в более поздней копией не оправдывает
        same = rerun_answer()
        find(same, "release-gate", 1)["run_attempt"] = 2
        self.stops("release-gate: runner group None", jobs=same)
        later = rerun_answer()
        find(later, "release-gate", 1)["run_attempt"] = 3
        self.stops("release-gate: runner group None", jobs=later)

    def test_rerun_copy_needs_attempt_number_and_group_name(self):
        no_attempt = rerun_answer()
        del find(no_attempt, "checks / check", 2)["run_attempt"]
        self.stops("checks / check: runner group None", jobs=no_attempt)
        wrong_name = rerun_answer()
        find(wrong_name, "checks / check", 2)["runner_group_name"] = "Self"
        self.stops("checks / check: runner group None / Self", jobs=wrong_name)
        flag = rerun_answer()
        find(flag, "checks / check", 2)["run_attempt"] = True  # bool - не номер попытки
        self.stops("checks / check: runner group None", jobs=flag)

    def test_null_group_without_attempts_stops(self):
        # без run_attempt (не перезапуск) null-группа - отказ, как и раньше
        jobs = good()
        jobs[2] = hosted("checks / build-image", runner_group_id=None)
        self.stops("checks / build-image: runner group None", jobs=jobs)

    def test_same_text_in_release_and_ci(self):
        self.assertEqual(step("release.yml", "release-attest")["run"], step("ci.yml", "runner-check")["run"])
        self.assertTrue(step("release.yml", "release-attest")["run"].isascii())


if __name__ == "__main__":
    rtlib.main()
