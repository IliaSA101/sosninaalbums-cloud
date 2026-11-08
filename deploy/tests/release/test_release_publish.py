"""release_publish_cases и mutate_release_artifact (design 004 §12): publish.sh и registry.sh с поддельным skopeo."""

import base64
import io
import json
import os
import shutil
import sys
import tarfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

REPO = "ghcr.io/iliasa101/albums-svc"
TAG = "v1.2.3"


class Publish(rtlib.Case):
    def setUp(self):
        super().setUp()
        self.state = self.path("state")
        os.makedirs(self.state)
        self.art = self.path("art")
        os.makedirs(self.art)
        self.archive = os.path.join(self.art, "albums-svc.oci.tar")
        self.digest, _ = rtlib.build_archive(self.archive)
        self.out = self.path("out")
        self.env = rtlib.shim_env(self.state, SA_OUT_DIR=self.out)

    def publish(self, digest=None, repo=REPO, tag=TAG, archive=None, **env):
        e = dict(self.env)
        e.update(env)
        return rtlib.sh(["bash", os.path.join(rtlib.REL, "publish.sh"), archive or self.archive, digest or self.digest, repo, tag], env=e)

    def st(self):
        with open(os.path.join(self.state, "state.json"), encoding="utf-8") as f:
            return json.load(f)

    def preload(self, ref, raw=b'{"schemaVersion":2,"other":1}'):
        d = "sha256:" + rtlib.sha(raw)
        with open(os.path.join(self.state, "state.json"), "w", encoding="utf-8") as f:
            json.dump({"tags": {ref: d}, "manifests": {d: base64.b64encode(raw).decode()}}, f)
        return d

    def test_first_push(self):
        rc, out = self.publish()
        self.assertEqual(rc, 0, out)
        st = self.st()
        self.assertEqual(st["tags"][f"{REPO}:{TAG}"], self.digest)
        with open(os.path.join(self.out, "release.manifest.json"), "rb") as f:
            manifest = f.read()
        self.assertEqual("sha256:" + rtlib.sha(manifest), self.digest)  # субъект аттестации: байты манифеста = D

    def test_repeat_same_digest(self):
        self.assertEqual(self.publish()[0], 0)
        rc, out = self.publish()
        self.assertEqual(rc, 0, out)
        self.assertIn("запись пропущена", out)

    def test_repeat_without_trailing_lf(self):
        rc, out = self.publish(SHIM_NO_LF="1")
        self.assertEqual(rc, 0, out)

    def test_tag_read_with_other_digest_is_refused(self):
        other = self.preload(f"{REPO}:{TAG}")
        rc, out = self.publish()
        self.assertEqual(rc, 1)
        self.assertIn("занят", out)
        self.assertEqual(self.st()["tags"][f"{REPO}:{TAG}"], other)  # ничего не записано поверх

    def test_tag_read_error_writes_and_verifies(self):
        rc, out = self.publish(SHIM_INSPECT_DENIED="1")
        # запись прошла, но решающая сверка «тег → D» тоже читает тег и не может: провал, аттестации не будет
        self.assertEqual(rc, 1, out)
        self.assertEqual(self.st()["tags"][f"{REPO}:{TAG}"], self.digest)
        self.assertIn("не прочитан", out)
        self.assertIn("не читается после записи", out)

    def test_tag_swapped_after_write_fails(self):
        rc, out = self.publish(SHIM_SWAP_AFTER_COPY="1")
        self.assertEqual(rc, 1, out)

    def test_wrong_digest_argument(self):
        rc, out = self.publish(digest="sha256:" + "0" * 64)
        self.assertEqual(rc, 1)
        self.assertIn("≠ D", out)
        self.assertFalse(os.path.exists(os.path.join(self.state, "state.json")))

    def test_two_manifests_in_index(self):
        a = self.path("two", "albums-svc.oci.tar")
        os.makedirs(os.path.dirname(a))
        d, _ = rtlib.build_archive(a, two_manifests=True)
        rc, out = self.publish(digest=d, archive=a)
        self.assertEqual(rc, 1)
        self.assertIn("ровно один", out)

    def test_artifact_not_single_file(self):
        with open(os.path.join(self.art, "extra.txt"), "w", encoding="utf-8") as f:
            f.write("x")
        rc, out = self.publish()
        self.assertEqual(rc, 1)
        self.assertIn("ровно один", out)

    def test_artifact_wrong_name_or_symlink(self):
        link = self.path("l", "albums-svc.oci.tar")
        os.makedirs(os.path.dirname(link))
        os.symlink(self.archive, link)
        rc, out = self.publish(archive=link)
        self.assertEqual(rc, 1)
        self.assertIn("не обычный файл", out)
        other = os.path.join(self.art, "x.tar")
        shutil.move(self.archive, other)
        self.assertEqual(self.publish(archive=other)[0], 1)

    def test_bad_arguments(self):
        for repo, tag in ((REPO + "x", TAG), (REPO, "v1.2"), (REPO, "v01.2.3"), (REPO, "latest"), ("127.0.0.1:5000/albums-svc", "stand")):
            self.assertEqual(self.publish(repo=repo, tag=tag)[0], 1, (repo, tag))
        self.assertEqual(self.publish(SA_DEST_TLS_VERIFY="true")[0], 1)
        self.assertFalse(os.path.exists(os.path.join(self.state, "state.json")))

    def test_stand_mode_only_for_stand_registry(self):
        rc, out = self.publish(repo="127.0.0.1:5000/albums-svc", tag="stand", SA_DEST_TLS_VERIFY="false")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.publish(repo=REPO, tag=TAG, SA_DEST_TLS_VERIFY="false")[0], 1)  # режим без TLS - не для GHCR
        self.assertEqual(self.publish(repo="127.0.0.1:5000/albums-svc", tag="v1.2.3", SA_DEST_TLS_VERIFY="false")[0], 1)


class MutateArtifact(rtlib.Case):
    """mutate_release_artifact: любая порча архива после build-image - отказ до записи."""

    def run_mutation(self, **kw):
        state = self.path("state")
        os.makedirs(state)
        art = self.path("art")
        os.makedirs(art)
        archive = os.path.join(art, "albums-svc.oci.tar")
        digest, _ = rtlib.build_archive(archive, **kw)
        return digest, rtlib.sh(
            ["bash", os.path.join(rtlib.REL, "publish.sh"), archive, digest, REPO, TAG],
            env=rtlib.shim_env(state, SA_OUT_DIR=self.path("out")),
        ), state

    def test_corrupted_manifest_blob_is_refused(self):
        # подменяем содержимое блоба манифеста при том же имени: index.json "тот же", а байты другие
        state = self.path("state")
        os.makedirs(state)
        art = self.path("art")
        os.makedirs(art)
        archive = os.path.join(art, "albums-svc.oci.tar")
        digest, _ = rtlib.build_archive(archive)
        src = archive + ".src"
        shutil.move(archive, src)
        with tarfile.open(src) as old, tarfile.open(archive, "w") as new:
            for m in old:
                data = old.extractfile(m).read()
                if m.name == "blobs/sha256/" + digest.split(":")[1]:
                    data = data.replace(b"schemaVersion", b"schemaVersioN")
                m.size = len(data)
                new.addfile(m, io.BytesIO(data))
        rc, out = rtlib.sh(["bash", os.path.join(rtlib.REL, "publish.sh"), archive, digest, REPO, TAG], env=rtlib.shim_env(state, SA_OUT_DIR=self.path("o")))
        self.assertEqual(rc, 1, out)
        self.assertFalse(os.path.exists(os.path.join(state, "state.json")))

    def test_layer_blob_corrupted(self):
        digest, (rc, out), state = self.run_mutation(corrupt_top=True)
        self.assertEqual(rc, 1, out)
        self.assertFalse(os.path.exists(os.path.join(state, "state.json")))


class Registry(rtlib.Case):
    DECOY = "ghs_DECOY_not_a_real_token_0123456789"

    def test_decoy_token_not_in_output_and_file_removed(self):
        state = self.path("state")
        os.makedirs(state)
        run = self.path("tmp")
        os.makedirs(run)
        env = rtlib.shim_env(state, RUNNER_TEMP=run, TOKEN=self.DECOY, GITHUB_ACTOR="octo-bot")
        script = os.path.join(rtlib.REL, "registry.sh")
        rc, out = rtlib.sh(["bash", script, "login"], env=env)
        self.assertEqual(rc, 0, out)
        self.assertNotIn(self.DECOY, out)
        auth = os.path.join(run, "auth.json")
        self.assertEqual(os.stat(auth).st_mode & 0o777, 0o600)
        rc, out = rtlib.sh(["bash", script, "logout"], env=env)
        self.assertEqual(rc, 0, out)
        self.assertNotIn(self.DECOY, out)
        self.assertFalse(os.path.exists(auth))  # файл входа удалён до загрузки публичного артефакта

    def test_logout_without_login_is_fine_and_bad_actor_refused(self):
        state = self.path("state")
        os.makedirs(state)
        run = self.path("tmp")
        os.makedirs(run)
        env = rtlib.shim_env(state, RUNNER_TEMP=run, TOKEN=self.DECOY, GITHUB_ACTOR="a b")
        script = os.path.join(rtlib.REL, "registry.sh")
        self.assertEqual(rtlib.sh(["bash", script, "logout"], env=env)[0], 0)
        self.assertEqual(rtlib.sh(["bash", script, "login"], env=env)[0], 1)


if __name__ == "__main__":
    rtlib.main()
