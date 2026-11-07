"""workflow_static (design 004 §12): каждое правило §2(б) - мутация на копии настоящих файлов даёт отказ.

Запускается в контейнере контроллера (PyYAML по хэшу). Плюс: настоящие файлы проходят, а набор действий
сверяется с --pins так, как это делает image-verify в ops-ВМ.
"""

import hashlib
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

guard = rtlib.load(os.path.join(rtlib.REL, "workflow_guard.py"), "workflow_guard")
W = ".github/workflows/"
GH_TOKEN = "          GH_TOKEN: ${{ github.token }}\n"
RELEASE_ATTEST_HEAD = "  release-attest:\n    needs: [release-gate, checks, release-publish]\n"
PUBLISH_PERMS = "      contents: read\n      packages: write\n"
DOWNLOAD = "          path: ${{ runner.temp }}/release-oci\n"
CHECKOUT_SHA = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
SETUP_GO = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
GATE_JOB = "  release-gate:\n    timeout-minutes: 10\n    runs-on: ubuntu-24.04\n"

# (правило, файл, что заменить, на что, подстрока в сообщении)
CASES = [
    ("vars.", "release.yml", GH_TOKEN, "          GH_TOKEN: ${{ vars.FOO }}\n", "вне перечня"),
    ("secrets.X", "release.yml", GH_TOKEN, "          GH_TOKEN: ${{ secrets.OTHER }}\n", "вне перечня"),
    ("secrets: inherit", "release.yml", "    uses: ./.github/workflows/checks.yml\n", "    uses: ./.github/workflows/checks.yml\n    secrets: inherit\n", "вне перечня"),
    ("environment: у release-attest", "release.yml", RELEASE_ATTEST_HEAD, RELEASE_ATTEST_HEAD + "    environment: release\n", "environment"),
    ("container:", "release.yml", GATE_JOB, GATE_JOB + "    container: ubuntu:24.04\n", "вне перечня"),
    ("services:", "release.yml", GATE_JOB, GATE_JOB + "    services:\n      db:\n        image: x\n", "вне перечня"),
    ("чужой runs-on", "release.yml", GATE_JOB, GATE_JOB.replace("ubuntu-24.04", "ubuntu-latest"), "runs-on"),
    ("self-hosted runs-on", "checks.yml", "  check:\n    timeout-minutes: 30\n    runs-on: ubuntu-24.04\n", "  check:\n    timeout-minutes: 30\n    runs-on: self-hosted\n", "runs-on"),
    ("${{ в run (одна строка)", "checks.yml", "        run: bash deploy/tests/provision/ci.sh prepare\n", "        run: echo ${{ github.ref }}\n", "в тексте run"),
    ("${{ в run (литерал)", "checks.yml", "        run: bash deploy/tests/provision/ci.sh prepare\n", "        run: |\n          echo ${{ github.ref }}\n", "в тексте run"),
    ("uses вне lock", "checks.yml", SETUP_GO, "actions/setup-go@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "нет в deploy/release/actions.lock"),
    ("тег в комментарии не из lock", "checks.yml", SETUP_GO + " # v7.0.0", SETUP_GO + " # v9.9.9", "тег в комментарии"),
    ("невидимый символ в имени", "checks.yml", "      - name: инварианты workflow\n", "      - name: инварианты" + chr(0x200B) + " workflow\n", "U+200B"),
    ("невидимый символ в комментарии", "ci.yml", "# Проверки идут", "# Проверки" + chr(0x202E) + " идут", "U+202E"),
    ("тире U+2013 в комментарии", "ci.yml", "# Проверки идут", "# Проверки " + chr(0x2013) + " идут", "вне белого списка"),
    ("NBSP в имени шага", "checks.yml", "      - name: инварианты workflow\n", "      - name: инварианты" + chr(0xA0) + "workflow\n", "вне белого списка"),
    ("галочка U+2713 в комментарии", "ci.yml", "# Проверки идут", "# Проверки " + chr(0x2713) + " идут", "вне белого списка"),
    ("типографская кавычка U+201C", "ci.yml", "# Проверки идут", "# Проверки " + chr(0x201C) + "идут", "вне белого списка"),
    ("не-ASCII в uses", "checks.yml", CHECKOUT_SHA, CHECKOUT_SHA.replace("actions", "a" + chr(0x441) + "tions"), "uses"),
    ("download-artifact: path вне runner.temp", "release.yml", DOWNLOAD, "          path: release-oci\n", "runner.temp"),
    ("download-artifact: run-id", "release.yml", DOWNLOAD, DOWNLOAD + "          run-id: 1\n", "вне перечня"),
    ("download-artifact: github-token", "release.yml", DOWNLOAD, DOWNLOAD + "          github-token: x\n", "вне перечня"),
    ("download-artifact: pattern", "release.yml", DOWNLOAD, DOWNLOAD + "          pattern: '*'\n", "вне перечня"),
    ("download-artifact: merge-multiple", "release.yml", DOWNLOAD, DOWNLOAD + "          merge-multiple: true\n", "вне перечня"),
    ("upload-artifact без if-no-files-found", "release.yml", "          path: ${{ runner.temp }}/release-manifest/release.manifest.json\n          if-no-files-found: error\n", "          path: ${{ runner.temp }}/release-manifest/release.manifest.json\n", "if-no-files-found"),
    ("upload-artifact: шаблон в path", "checks.yml", "          path: ${{ runner.temp }}/oci/albums-svc.oci.tar\n", "          path: ${{ runner.temp }}/oci/*\n", "ровно один файл"),
    ("actions/cache", "checks.yml", CHECKOUT_SHA, "actions/cache@3d3c42e5aac5ba805825da76410c181273ba90b1", "uses"),
    ("setup-go с кэшем", "checks.yml", "          cache: false\n", "          cache: true\n", "cache"),
    ("latest", "checks.yml", "golangci-lint@v2.15.0", "golangci-lint@latest", "latest"),
    ("лишнее право release-publish", "release.yml", PUBLISH_PERMS, PUBLISH_PERMS + "      id-token: write\n", "права должны быть ровно"),
    ("needs release-attest", "release.yml", RELEASE_ATTEST_HEAD, "  release-attest:\n    needs: [release-gate, checks]\n", "needs"),
    ("workflow_dispatch в release.yml", "release.yml", "on:\n  push:\n", "on:\n  workflow_dispatch:\n  push:\n", "on -"),
    ("шаблон метки", "release.yml", 'tags: ["v[0-9]+.[0-9]+.[0-9]+"]', 'tags: ["v*"]', "on -"),
    ("permissions: {} наверху", "release.yml", "permissions: {}\n", "permissions:\n  contents: read\n", "permissions"),
    ("выход digest checks.yml", "checks.yml", "value: ${{ jobs.build-image.outputs.digest }}", "value: ${{ inputs.version }}", "outputs.digest"),
    ("D из release-publish", "release.yml", "subject-digest: ${{ needs.checks.outputs.digest }}", "subject-digest: ${{ steps.attest.outputs.bundle-path }}", "attest"),
    ("порядок шагов release-publish", "release.yml", "registry.sh logout", "registry.sh login", "эталону guard"),
    ("if: на шаге стенда", "checks.yml", "      - name: idempotence_full\n", "      - name: idempotence_full\n        if: always()\n", "if:"),
    ("if: у задания выпуска", "release.yml", GATE_JOB, GATE_JOB + "    if: false\n", "вне перечня"),
    ("право write вне release.yml", "ci.yml", "  probe:\n", "  probe:\n    permissions:\n      packages: write\n", "write"),
    ("нет timeout-minutes", "checks.yml", "  check:\n    timeout-minutes: 30\n", "  check:\n", "timeout-minutes"),
    ("checkout в release-attest", "release.yml", "      - uses: actions/attest@", "      - uses: " + CHECKOUT_SHA + " # v7.0.1\n        with:\n          persist-credentials: false\n      - uses: actions/attest@", "закреплено целиком"),
    ("checkout без persist-credentials: false", "release.yml", "          fetch-depth: 0\n          persist-credentials: false\n", "          fetch-depth: 0\n", "persist-credentials"),
    ("сверка раннеров ci.yml ≠ release.yml", "ci.yml", "GROUP_ID = 0", "GROUP_ID = 1", "эталону guard"),
    ("run сверки раннеров не ASCII", "release.yml", "import json, os, sys", "import json, os, sys  # " + chr(0x44F), "не-ASCII"),
]


class Static(rtlib.Case):
    def tree(self, muts=()):
        self.n = getattr(self, "n", 0) + 1
        root = self.path(f"tree{self.n}")
        rtlib.copy_tree(root)
        for rel, old, new in muts:
            p = os.path.join(root, rel)
            rtlib.write(p, rtlib.sub(rtlib.read(p), old, new))
        return root

    def test_real_files_pass(self):
        self.assertEqual(rtlib.check(guard, rtlib.ROOT), [])

    def test_each_rule_has_a_failing_mutation(self):
        for name, fname, old, new, want in CASES:
            with self.subTest(name):
                errs = rtlib.check(guard, self.tree([(W + fname, old, new)]))
                self.assertTrue(errs, f"мутация «{name}» не отклонена")
                self.assertTrue(any(want in e for e in errs), f"{name}: нет «{want}» в {errs}")

    def test_unknown_file_in_github(self):
        root = self.tree()
        rtlib.write(os.path.join(root, ".github", "dependabot.yml"), "version: 2\n")
        self.assertTrue(any("незнакомый файл" in e for e in rtlib.check(guard, root)))
        root = self.tree()
        rtlib.write(os.path.join(root, W + "evil.yml"), "on: push\n")
        self.assertTrue(any("evil.yml" in e for e in rtlib.check(guard, root)))

    def test_download_artifact_older_than_fixed_version_is_refused(self):
        root = self.tree([("deploy/release/actions.lock", "actions/download-artifact v8.0.2", "actions/download-artifact v4.1.2"),
                          (W + "release.yml", "# v8.0.2", "# v4.1.2")])
        self.assertTrue(any("CVE-2024-42471" in e for e in rtlib.check(guard, root)))

    def test_pins_dir_requires_checked_lock(self):
        root = self.tree()
        pins = self.path("pins")
        os.makedirs(pins)
        errs = rtlib.check(guard, root, pins)
        self.assertTrue(any("не сверен" in e and "окно Б" in e for e in errs), errs)
        with open(os.path.join(root, "deploy/release/actions.lock"), "rb") as f:
            sha = hashlib.sha256(f.read()).hexdigest()
        rtlib.write(os.path.join(pins, f"{sha}.lock"), "x")
        self.assertEqual(rtlib.check(guard, root, pins), [])
        # lock изменён после сверки (другой sha256) - снова отказ
        p = os.path.join(root, "deploy/release/actions.lock")
        rtlib.write(p, rtlib.read(p) + "# правка\n")
        self.assertTrue(any("не сверен" in e for e in rtlib.check(guard, root, pins)))

    def test_lock_syntax(self):
        root = self.tree()
        p = os.path.join(root, "deploy/release/actions.lock")
        rtlib.write(p, rtlib.read(p) + "docker/login-action v3.0.0 " + "a" * 40 + "\n")
        self.assertTrue(any("actions.lock" in e for e in rtlib.check(guard, root)))


if __name__ == "__main__":
    rtlib.main()
