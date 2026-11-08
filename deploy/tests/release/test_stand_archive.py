"""stand_archive_mode (design 004 §12): режим архива у стенда - в реестре ровно D; другой D - отказ."""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

FULL = os.path.join(rtlib.ROOT, "deploy", "tests", "provision", "full.sh")


class StandArchive(rtlib.Case):
    def setUp(self):
        super().setUp()
        self.state = self.path("state")
        os.makedirs(self.state)
        self.art = self.path("art")
        os.makedirs(self.art)
        self.archive = os.path.join(self.art, "albums-svc.oci.tar")
        self.digest, _ = rtlib.build_archive(self.archive)

    def push(self, digest, archive=None, with_skopeo=True):
        env = rtlib.shim_env(self.state)
        env["SA_IMAGE_ARCHIVE"] = archive or self.archive
        env["root"] = rtlib.ROOT
        if digest is not None:
            env["SA_IMAGE_DIGEST"] = digest
        if not with_skopeo:
            env["PATH"] = "/usr/bin:/bin"
        # full.sh при загрузке только объявляет функции: берём ровно full_push_archive, как её зовёт step_converge_image
        return rtlib.sh(["bash", "-c", '. "$1"; full_push_archive 5000 "$2"', "x", FULL, self.path("manifest")], env=env)

    def test_archive_with_d_lands_exactly(self):
        rc, out = self.push(self.digest)
        self.assertEqual(rc, 0, out)
        with open(os.path.join(self.state, "state.json"), encoding="utf-8") as f:
            st = json.load(f)
        self.assertEqual(st["tags"], {"127.0.0.1:5000/albums-svc:stand": self.digest})

    def test_other_d_is_refused_and_nothing_written(self):
        rc, out = self.push("sha256:" + "a" * 64)
        self.assertEqual(rc, 1, out)
        self.assertFalse(os.path.exists(os.path.join(self.state, "state.json")))

    def test_missing_or_bad_d(self):
        self.assertEqual(self.push(None)[0], 1)
        self.assertEqual(self.push("latest")[0], 1)

    def test_archive_replaced_after_build(self):
        other = self.path("art2", "albums-svc.oci.tar")
        os.makedirs(os.path.dirname(other))
        rtlib.build_archive(other, labels={"org.example.x": "1"})
        rc, out = self.push(self.digest, archive=other)
        self.assertEqual(rc, 1, out)
        self.assertIn("≠ D", out)

    def test_without_skopeo_it_says_so(self):
        rc, out = self.push(self.digest, with_skopeo=False)
        self.assertEqual(rc, 1)
        self.assertIn("skopeo", out)


if __name__ == "__main__":
    rtlib.main()
