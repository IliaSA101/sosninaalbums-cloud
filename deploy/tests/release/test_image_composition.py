"""image_composition (design 004 §12): эталон состава образа ловит лишний файл, чужие Env и метки, пути машины."""

import os
import shutil
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

CONTAINERFILE = os.path.join(rtlib.ROOT, "deploy", "Containerfile")
BIN = ("albums-svc", b"\x7fELF-synthetic", 0o555, 0, 0)


class Composition(rtlib.Case):
    def check(self, version="1.2.3", **kw):
        arch = self.path("a.oci.tar")
        _, insp = rtlib.build_archive(arch, inspect=self.path("base.json"), **kw)
        return rtlib.sh(
            [
                "python3", "-I", os.path.join(rtlib.REL, "image_check.py"), arch, "--containerfile", CONTAINERFILE,
                "--base-inspect", insp, "--version", version, "--revision", rtlib.OWN["org.opencontainers.image.revision"],
            ]
        )

    def bad(self, want, **kw):
        rc, out = self.check(**kw)
        self.assertEqual(rc, 1, out)
        self.assertIn(want, out)

    def test_good_image_passes(self):
        rc, out = self.check()
        self.assertEqual(rc, 0, out)

    def test_extra_file_in_top_layer(self):
        self.bad("ожидался один файл", top_files=[BIN, ("extra", b"x", 0o644, 0, 0)])

    def test_wrong_file_name_mode_owner(self):
        self.bad("ожидался один файл", top_files=[("other", b"x", 0o555, 0, 0)])
        self.bad("нужны 0555", top_files=[("albums-svc", b"x", 0o755, 0, 0)])
        self.bad("нужен 0:0", top_files=[("albums-svc", b"x", 0o555, 65532, 65532)])

    def test_path_tricks_in_top_layer_name(self):
        for name in ("../albums-svc", "/albums-svc", ".albums-svc", "..albums-svc", "./../albums-svc"):
            self.bad("ожидался один файл", top_files=[(name, b"x", 0o555, 0, 0)])

    def test_foreign_env(self):
        self.bad("Env не равен", env=rtlib.BASE_ENV + ["EVIL=1"])

    def test_extra_or_changed_label(self):
        self.bad("метки не по эталону", labels={"org.example.extra": "1"})
        self.bad("метки не по эталону", labels={"org.opencontainers.image.licenses": "MIT"})
        self.bad("метки не по эталону", drop_labels=["org.opencontainers.image.title"])

    def test_buildah_label_is_allowed(self):
        rc, out = self.check(labels={"io.buildah.version": "1.33.7"})
        self.assertEqual(rc, 0, out)

    def test_runner_path_in_history(self):
        # Домашний каталог раннера собран из частей: целиком такая строка в публичном файле - примета личного пути.
        self.bad("путь машины сборки", history="RUN cp /" + "home" + "/ci/work/x /albums-svc")
        self.bad("путь машины сборки", history="RUN cp /runner/work/x /albums-svc")
        self.bad("путь машины сборки", history="ENV RUNNER_TEMP=/x")

    def test_layers_differ_from_base(self):
        self.bad("первые слои не равны", first_diff="sha256:" + "1" * 64)

    def test_user_entrypoint_cmd(self):
        self.bad("User", user="0:0")
        self.bad("Entrypoint", entrypoint=["/bin/sh"])
        self.bad("Cmd", cmd=["-x"])

    def test_two_manifests_and_wrong_version(self):
        self.bad("не удалось прочитать", two_manifests=True)
        rc, out = self.check(version="9.9.9")  # метка version в образе - 1.2.3, а выпуск заявлен как 9.9.9
        self.assertEqual(rc, 1, out)
        self.assertIn("метки не по эталону", out)


class BuildScript(rtlib.Case):
    """build.sh целиком, с поддельным podman: версия из метки, дайджест в GITHUB_OUTPUT, эталон до вывода."""

    def build(self, ref="refs/tags/v1.2.3", pre_existing=False, **kw):
        state = self.path("state")
        os.makedirs(state)
        src = self.path("src.oci.tar")
        _, insp = rtlib.build_archive(src, inspect=self.path("base.json"), **kw)
        out_dir = self.path("oci")
        os.makedirs(out_dir)
        if pre_existing:
            with open(os.path.join(out_dir, "old"), "w", encoding="utf-8") as f:
                f.write("x")
        gh_out = self.path("gh-output")
        env = rtlib.shim_env(state, SHIM_ARCHIVE_SRC=src, SHIM_INSPECT=insp, SA_VERSION_REF=ref, GITHUB_OUTPUT=gh_out, RUNNER_TEMP=self.tmp,
                             GITHUB_SHA=rtlib.OWN["org.opencontainers.image.revision"])
        rc, text = rtlib.sh(["bash", os.path.join(rtlib.REL, "build.sh"), os.path.join(out_dir, "albums-svc.oci.tar")], env=env)
        out = ""
        if os.path.exists(gh_out):
            with open(gh_out, encoding="utf-8") as f:
                out = f.read()
        return rc, text, out, state

    def test_tag_build_passes_and_exports_digest(self):
        rc, text, out, state = self.build()
        self.assertEqual(rc, 0, text)
        self.assertRegex(out, r"^digest=sha256:[0-9a-f]{64}\n$")
        with open(os.path.join(state, "build.args"), encoding="utf-8") as f:
            args = f.read()
        self.assertIn("SA_VERSION=1.2.3", args)
        self.assertIn("SA_REVISION=" + rtlib.OWN["org.opencontainers.image.revision"], args)

    def test_ci_build(self):
        rc, text, _, _ = self.build(ref="ci", labels={"org.opencontainers.image.version": "ci"})
        self.assertEqual(rc, 0, text)

    def test_bad_ref_is_refused_before_build(self):
        for ref in ("refs/heads/main", "refs/tags/v1.2", "refs/tags/v1.2.3;id", "v1.2.3"):
            rc, text, out, state = self.build(ref=ref)
            self.assertEqual(rc, 1, (ref, text))
            self.assertEqual(out, "")
            self.assertFalse(os.path.exists(os.path.join(state, "build.args")))
            for d in ("state", "oci"):
                shutil.rmtree(self.path(d))

    def test_image_off_standard_gives_no_output(self):
        rc, text, out, _ = self.build(top_files=[("albums-svc", b"x", 0o555, 0, 0), ("extra", b"x", 0o644, 0, 0)])
        self.assertEqual(rc, 1, text)
        self.assertEqual(out, "")
        self.assertIn("состав образа не по эталону", text)

    def test_non_empty_archive_dir_is_refused(self):
        rc, text, _, _ = self.build(pre_existing=True)
        self.assertEqual(rc, 1, text)


if __name__ == "__main__":
    rtlib.main()
