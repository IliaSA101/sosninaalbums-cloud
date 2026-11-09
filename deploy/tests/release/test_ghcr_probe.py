"""ghcr_probe_cases (design 004 §11-§12, решение 004-17): проба сети сервера до реестра образов и предел `pull`.

Реестр - заглушка на петлевом адресе (stdlib): выдача анонимного токена по 401, манифест и блобы по дайджесту, блобы
отдаёт «хранилище слоёв» на другом порту через 307 (как pkg-containers.githubusercontent.com у GHCR). Сеть не нужна.
Проба запускается как на сервере: `python3 -I ghcr_probe.py <образ>`. Тестовые слои - синтетические данные, не фото.
"""

import json
import os
import re
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.dont_write_bytecode = True  # до import rtlib: иначе его байт-код ляжет в дерево, которое копирует стенд
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rtlib  # noqa: E402

PROBE = os.path.join(rtlib.ROOT, "deploy", "ops", "ghcr_probe.py")
TOKEN = "decoydecoydecoydecoy"  # заглушка (энтропия Шеннона 2,3: gitleaks generic-api-key не срабатывает); в вывод пробы попасть не должна
NAME = "example/albums-svc"
SIZES = [100 * 1024, 40 * 1024, 7 * 1024]  # слои заведомо больше 16 КиБ (граница прежней обрезки)


def layer_bytes(i, n):
    """Однотонные псевдослучайные данные слоя i (повторение блока): воспроизводимо и не похоже на реальные файлы."""
    block = rtlib.sha(f"layer-{i}".encode()).encode() * 4
    return (block * (n // len(block) + 1))[:n]


class World:
    """Состояние заглушки: образ и режимы отказов (меняются тестами)."""

    def __init__(self):
        self.blobs = {}
        layers = []
        for i, n in enumerate(SIZES):
            data = layer_bytes(i, n)
            d = "sha256:" + rtlib.sha(data)
            self.blobs[d] = data
            layers.append({"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": d, "size": n})
        cfg = json.dumps({"architecture": "amd64", "os": "linux"}).encode()
        cd = "sha256:" + rtlib.sha(cfg)
        self.blobs[cd] = cfg
        self.set_manifest({"schemaVersion": 2, "mediaType": rtlib.OCI_MANIFEST,
                           "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cd, "size": len(cfg)},
                           "layers": layers})
        self.layer_digests = [layer["digest"] for layer in layers]
        self.mode = ""  # режим отказа CDN/реестра
        self.cut = 16384
        self.reg_auth = []  # заголовки Authorization запросов к реестру (блобы)
        self.cdn_auth = []  # то же у «хранилища слоёв»: токена там быть не должно
        self.token_requests = 0
        self.cdn_port = 0
        self.reg_port = 0

    def set_manifest(self, doc):
        self.manifest = json.dumps(doc, sort_keys=True).encode()
        self.digest = "sha256:" + rtlib.sha(self.manifest)

    @property
    def ref(self):
        return f"127.0.0.1:{self.reg_port}/{NAME}@{self.digest}"


def make_handler(world, kind):
    class H(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def send(self, code, body=b"", headers=()):
            self.send_response(code)
            for k, v in headers:
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def serve_blob(self, data):
            """Тело блоба с отказом по режиму: обрыв на 16 КиБ при полном Content-Length, честно короткий, чужие байты, медленно."""
            m = world.mode
            if m == "cut_lying":  # 200, заявлено всё, отдано 16 КиБ и закрыто - прежняя обрезка из ВР-16
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data[: world.cut])
                self.wfile.flush()
                self.close_connection = True
                return
            if m == "cut_honest":  # 200, Content-Length равен обрезанному телу: только размер из манифеста выдаёт
                data = data[: world.cut]
            elif m == "flip":  # тот же размер, другие байты
                data = bytes([data[0] ^ 1]) + data[1:]
            elif m == "slow":  # байт за байтом: скорость ниже порога
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                try:
                    for i in range(0, min(4000, len(data)), 40):
                        self.wfile.write(data[i : i + 40])
                        self.wfile.flush()
                        time.sleep(0.05)
                except OSError:
                    pass
                self.close_connection = True
                return
            elif m == "trickle" and len(data) > 1000:  # слои (конфигурация отдаётся сразу); по байту раз в 0,1 с, соединение не закрывается: каждый recv короче любого тайм-аута сокета
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                try:
                    for i in range(200):
                        self.wfile.write(data[i : i + 1])
                        self.wfile.flush()
                        time.sleep(0.1)
                except OSError:
                    pass
                self.close_connection = True
                return
            elif m == "cl_super":  # Content-Length из «цифр» вне ASCII: isdigit() истинно, int() падает
                self.send_response(200)
                self.send_header("Content-Length", "\u00b2")
                self.end_headers()
                self.wfile.write(data)
                self.close_connection = True
                return
            elif m == "stall":  # заголовки есть, тела нет
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.flush()
                time.sleep(6)
                self.close_connection = True
                return
            self.send(200, data, [("Content-Type", "application/octet-stream")])

        def do_GET(self):  # noqa: N802
            p = self.path.split("?")[0]
            if kind == "cdn":
                world.cdn_auth.append(self.headers.get("Authorization"))
                d = p.rsplit("/", 1)[-1]
                if d in world.blobs:
                    return self.serve_blob(world.blobs[d])
                return self.send(404)
            if p == "/token":
                world.token_requests += 1
                body = json.dumps({"token": "x y" if world.mode == "token_bad" else TOKEN}).encode()
                if world.mode == "token_deep":  # глубокая вложенность в пределе 64 КиБ: json.loads упирается в рекурсию
                    body = b"[" * 60000
                return self.send(200, body, [("Content-Type", "application/json")])
            m = re.match(r"^/v2/(.+)/(manifests|blobs)/(sha256:[0-9a-f]{64})$", p)
            if not m or m.group(1) != NAME:
                return self.send(404)
            if self.headers.get("Authorization") != "Bearer " + TOKEN and world.mode != "open":
                realm = f"http://127.0.0.1:{world.reg_port}/token"
                if world.mode == "realm_other":
                    realm = f"http://127.0.0.2:{world.reg_port}/token"
                return self.send(401, b"{}", [("WWW-Authenticate", f'Bearer realm="{realm}",service="stub",scope="repository:{NAME}:pull"')])
            if m.group(2) == "manifests":
                if m.group(3) != world.digest:
                    return self.send(404)
                body = world.manifest
                if world.mode == "manifest_swapped":  # отдан другой манифест под тем же адресом
                    body = body.replace(b"amd64", b"arm64") if b"amd64" in body else body + b" "
                return self.send(200, body, [("Content-Type", rtlib.OCI_MANIFEST)])
            world.reg_auth.append(self.headers.get("Authorization"))
            if m.group(3) not in world.blobs:
                return self.send(404)
            if world.mode == "redirect_http_other":  # с петли на непетлевой узел по http
                return self.send(307, b"", [("Location", f"http://registry.example.test:{world.cdn_port}/cdn/{m.group(3)}")])
            if world.mode == "redirect_badport":
                return self.send(307, b"", [("Location", f"http://127.0.0.1:abc/cdn/{m.group(3)}")])
            if world.mode == "redirect_https":
                return self.send(307, b"", [("Location", f"https://127.0.0.1:{world.cdn_port}/cdn/{m.group(3)}")])
            if world.mode == "no_redirect":
                return self.serve_blob(world.blobs[m.group(3)])
            return self.send(307, b"", [("Location", f"http://127.0.0.1:{world.cdn_port}/cdn/{m.group(3)}?sig=abc")])

    return H


class Servers:
    def __init__(self, world):
        self.world = world
        self.srv = []
        for kind in ("reg", "cdn"):
            s = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(world, "cdn" if kind == "cdn" else "reg"))
            s.daemon_threads = True
            threading.Thread(target=s.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True).start()
            self.srv.append(s)
        world.reg_port = self.srv[0].server_address[1]
        world.cdn_port = self.srv[1].server_address[1]

    def close(self):
        for s in self.srv:
            s.shutdown()
            s.server_close()


class Probe(rtlib.Case):
    def setUp(self):
        super().setUp()
        self.w = World()
        self.srv = Servers(self.w)
        self.addCleanup(self.srv.close)

    def run_probe(self, ref=None, *extra):
        return rtlib.sh([sys.executable, "-I", PROBE, ref or self.w.ref, *extra])

    def fails(self, *extra, want=None):
        rc, out = self.run_probe(None, *extra)
        self.assertEqual(rc, 1, out)
        self.assertIn("образ не получен целиком", out)
        if want:
            self.assertIn(want, out)
        self.assertNotIn(TOKEN, out)
        return out

    # ---- успех ----

    def test_success_whole_image(self):
        rc, out = self.run_probe()
        self.assertEqual(rc, 0, out)
        self.assertIn("ghcr_probe: ok, блобов 4", out)
        self.assertNotIn(TOKEN, out)
        self.assertEqual(self.w.token_requests, 1)

    def test_token_only_to_registry_not_to_layer_storage(self):
        self.assertEqual(self.run_probe()[0], 0)
        self.assertTrue(self.w.reg_auth and all(a == "Bearer " + TOKEN for a in self.w.reg_auth), self.w.reg_auth)
        self.assertTrue(self.w.cdn_auth and all(a is None for a in self.w.cdn_auth), self.w.cdn_auth)

    def test_registry_without_auth_works(self):
        self.w.mode = "open"
        rc, out = self.run_probe()
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.w.token_requests, 0)

    def test_blob_served_directly_without_redirect(self):
        self.w.mode = "no_redirect"
        self.assertEqual(self.run_probe()[0], 0)

    # ---- неполное скачивание и подмена ----

    def test_cut_at_16k_with_full_content_length(self):
        self.w.mode = "cut_lying"
        self.fails(want="обрыв")

    def test_cut_at_16k_with_honest_short_content_length(self):
        self.w.mode = "cut_honest"
        self.fails(want="получено 16384 байт из")

    def test_sha256_mismatch_same_size(self):
        self.w.mode = "flip"
        self.fails(want="sha256 не сошёлся")

    def test_manifest_bytes_not_equal_to_digest(self):
        self.w.mode = "manifest_swapped"
        self.fails(want="манифест: sha256 не равен дайджесту")

    def test_cut_mode_applies_after_ok_without_it(self):
        # Контроль: тот же стенд без режима отказа проходит, с режимом - нет (тест не «красный всегда»).
        self.assertEqual(self.run_probe()[0], 0)
        self.w.mode = "cut_lying"
        self.fails()

    # ---- нет ответа, медленно ----

    def test_no_answer_fails_by_deadline(self):
        # Сокет принимает соединение и молчит: проба обязана уложиться в предел времени, а не висеть.
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        s.listen(4)
        self.addCleanup(s.close)
        ref = f"127.0.0.1:{s.getsockname()[1]}/{NAME}@{self.w.digest}"
        t0 = time.monotonic()
        rc, out = self.run_probe(ref, "--deadline", "3")
        dt = time.monotonic() - t0
        self.assertEqual(rc, 1, out)
        self.assertIn("образ не получен целиком", out)
        self.assertLess(dt, 15, f"проба держалась {dt:.1f} с при пределе 3 с")

    def test_stalled_body_fails_by_deadline(self):
        self.w.mode = "stall"
        t0 = time.monotonic()
        rc, out = self.run_probe(None, "--deadline", "2")
        self.assertEqual(rc, 1, out)
        self.assertIn("образ не получен целиком", out)
        self.assertLess(time.monotonic() - t0, 10)

    def test_connection_refused_fails(self):
        s = socket.socket()
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
        s.close()
        rc, out = self.run_probe(f"127.0.0.1:{port}/{NAME}@{self.w.digest}", "--deadline", "5")
        self.assertEqual(rc, 1, out)

    def test_min_speed(self):
        # Порог скорости - в процессе теста (в сценарии GRACE 15 с; здесь 0,3 с): ~800 байт/с против порога 100000.
        mod = rtlib.load(PROBE, "ghcr_probe_speed")
        mod.GRACE = 0.3
        self.w.mode = "slow"
        p = mod.Probe(self.w.ref, 30, 100000)
        with self.assertRaises(mod.Fail) as cm:
            p.run()
        self.assertIn("скорость ниже", str(cm.exception))

    def test_trickle_without_closing_fails_by_deadline(self):
        # Байт без закрытия соединения: прежний read(n) крутил recv до 64 КиБ мимо tick() и держал пробу до внешнего тайм-аута.
        # Интервал 0,1 с короче минимального тайм-аута сокета (0,2 с): после срока спасает только tick(), поэтому тест краснеет
        # и на мутации «убрать проверку срока в tick» (с интервалом 0,5 с её подменил бы тайм-аут сокета).
        self.w.mode = "trickle"
        t0 = time.monotonic()
        rc, out = self.run_probe(None, "--deadline", "3")
        dt = time.monotonic() - t0
        self.assertEqual(rc, 1, out)
        self.assertIn("предел времени исчерпан", out)
        self.assertLess(dt, 12, f"проба держалась {dt:.1f} с при пределе 3 с")

    # ---- адреса и схемы ----

    def test_realm_on_other_host_is_refused(self):
        self.w.mode = "realm_other"
        self.fails(want="адрес выдачи токена не на узле реестра")
        self.assertEqual(self.w.token_requests, 0)

    def test_redirect_to_other_scheme_is_refused(self):
        self.w.mode = "redirect_https"
        self.fails(want="недопустимый адрес")

    def test_redirect_by_http_to_non_loopback_is_refused(self):
        self.w.mode = "redirect_http_other"
        self.fails(want="по http на непетлевой узел")

    def test_bad_port_in_location_is_fail_not_traceback(self):
        self.w.mode = "redirect_badport"
        out = self.fails(want="не разбираются")
        self.assertNotIn("Traceback", out)

    def test_content_length_from_non_ascii_digits_is_fail(self):
        self.w.mode = "cl_super"
        out = self.fails(want="Content-Length не число")
        self.assertNotIn("Traceback", out)

    def test_deeply_nested_token_response_is_fail(self):
        self.w.mode = "token_deep"
        out = self.fails(want="не JSON")
        self.assertNotIn("Traceback", out)

    def test_token_with_bad_characters_is_refused(self):
        self.w.mode = "token_bad"
        self.fails(want="недопустимые знаки")

    def test_scheme_by_host(self):
        mod = rtlib.load(PROBE, "ghcr_probe_scheme")
        d = "sha256:" + "a" * 64
        self.assertEqual(mod.Probe(f"ghcr.io/example/x@{d}", 5, 1).scheme, "https")
        self.assertEqual(mod.Probe(f"example.org:5000/example/x@{d}", 5, 1).scheme, "https")
        self.assertEqual(mod.Probe(f"127.0.0.1:5000/example/x@{d}", 5, 1).scheme, "http")
        self.assertEqual(mod.Probe(f"localhost:5000/example/x@{d}", 5, 1).scheme, "http")
        self.assertEqual(mod.Probe(f"127.0.0.1.example.org/example/x@{d}", 5, 1).scheme, "https")

    # ---- манифест ----

    def reset_manifest(self, mutate):
        doc = json.loads(self.w.manifest)
        mutate(doc)
        self.w.set_manifest(doc)

    def test_index_instead_of_manifest_is_refused(self):
        self.reset_manifest(lambda d: d.update({"manifests": [{"digest": "sha256:" + "0" * 64, "size": 1}]}))
        self.fails(want="не похож на манифест образа")

    def test_too_many_layers_is_refused(self):
        self.reset_manifest(lambda d: d.update({"layers": d["layers"] * 70}))
        self.fails(want="слишком много слоёв")

    def test_huge_blob_in_manifest_is_refused_before_download(self):
        def big(d):
            d["layers"][0]["size"] = 10 << 30

        self.reset_manifest(big)
        self.fails(want="не похож на манифест образа")
        self.assertEqual(self.w.reg_auth, [])

    def test_manifest_with_bad_digest_field_is_refused(self):
        self.reset_manifest(lambda d: d["layers"][0].update({"digest": "sha256:xyz"}))
        self.fails(want="не похож на манифест образа")

    def test_unknown_media_type_is_refused(self):
        self.reset_manifest(lambda d: d.update({"mediaType": "text/html"}))
        self.fails(want="не похож на манифест образа")

    # ---- вызов ----

    def test_usage_errors_exit_2(self):
        d = "sha256:" + "a" * 64
        for args in ([], ["не-образ"], [f"ghcr.io/x@{d.upper()}"], [f"ghcr.io/x@{d}", "--deadline"],
                     [f"ghcr.io/x@{d}", "--deadline", "0"], [f"ghcr.io/x@{d}", "--deadline", "99999"],
                     [f"ghcr.io/x@{d}", "--bogus", "1"], [f"ghcr.io/x:latest@{d}x"]):
            rc, out = rtlib.sh([sys.executable, "-I", PROBE, *args])
            self.assertEqual(rc, 2, f"{args}: {out}")


class RealConsts(rtlib.Case):
    """Боевой image-consts.env: образ в команде пробы строгого вида (IC_SUBJECT в iv_consts проверен лишь по набору знаков)."""

    def test_subject_has_strict_registry_form(self):
        text = rtlib.read(os.path.join(rtlib.ROOT, "deploy", "ops", "image-consts.env"))
        m = re.search(r"^IC_SUBJECT=(\S+)$", text, re.M)
        self.assertIsNotNone(m)
        mod = rtlib.load(PROBE, "ghcr_probe_consts")
        self.assertTrue(mod.REF_RE.match(m.group(1) + "@sha256:" + "a" * 64), m.group(1))
        # та же форма, что ghcr_ref_re в ops/probe.sh (проверка img_ref перед отправкой на сервер)
        sh_re = re.search(r"^ghcr_ref_re='(.+)'$", rtlib.read(os.path.join(rtlib.ROOT, "deploy", "ops", "probe.sh")), re.M)
        self.assertIsNotNone(sh_re)
        self.assertTrue(re.match(sh_re.group(1), m.group(1) + "@sha256:" + "a" * 64))
        self.assertFalse(re.match(sh_re.group(1), "x/y z@sha256:" + "a" * 64))
        self.assertFalse(re.match(sh_re.group(1), "x/y'$(id)@sha256:" + "a" * 64))


class PullLimit(rtlib.Case):
    """Предел `pull` в роли сервиса: явный, из переменной с границами, и сам `pull` идёт под `timeout`."""

    RUNTIME = os.path.join(rtlib.ROOT, "deploy", "ansible", "roles", "albums_svc", "tasks", "runtime.yml")
    DEFAULTS = os.path.join(rtlib.ROOT, "deploy", "ansible", "roles", "albums_svc", "defaults", "main.yml")
    ASSERT = os.path.join(rtlib.ROOT, "deploy", "ansible", "roles", "albums_svc", "tasks", "assert.yml")

    def pull_task(self):
        text = rtlib.read(self.RUNTIME)
        m = re.search(r"- name: Образ сервиса по дайджесту \(с пределом времени\)\n(.*?)(?=\n\n)", text, re.S)
        self.assertIsNotNone(m, "задача pull не найдена")
        return m.group(1)

    def test_pull_runs_under_timeout_with_variable(self):
        t = self.pull_task()
        self.assertRegex(t, r"argv: \[timeout, --kill-after=\d+, \"\{\{ albums_svc_pull_timeout_sec \}\}\", podman, pull, ")
        self.assertIn("albums_svc_image", t)

    def test_pull_failure_names_the_limit(self):
        t = self.pull_task()
        # Сама задача не падает (rc читает следующая), а следующая называет предел при коде 124 от timeout.
        self.assertIn("failed_when: false", t)
        text = rtlib.read(self.RUNTIME)
        self.assertRegex(text, r"- name: Образ скачан в пределе времени\n(?:.*\n)*?.*albums_svc_pull_res is skipped or albums_svc_pull_res\.rc == 0")
        self.assertIn("albums_svc_pull_res.rc | default(0) in [124, 137]", text)

    def test_timeout_default_and_bounds(self):
        m = re.search(r"^albums_svc_pull_timeout_sec: (\d+)$", rtlib.read(self.DEFAULTS), re.M)
        self.assertIsNotNone(m, "нет albums_svc_pull_timeout_sec в defaults")
        self.assertTrue(60 <= int(m.group(1)) <= 3600)
        a = rtlib.read(self.ASSERT)
        self.assertRegex(a, r"albums_svc_pull_timeout_sec is integer")
        self.assertRegex(a, r"albums_svc_pull_timeout_sec >= \d+")
        self.assertRegex(a, r"albums_svc_pull_timeout_sec <= \d+")

    def test_probe_deadline_matches_pull_default(self):
        # Один предел на проверку «скачивается ли» и на сам pull: до замера на пробной аренде - 600 с (design §11).
        mod = rtlib.load(PROBE, "ghcr_probe_defaults")
        ref, deadline, min_bps = mod.parse(["x", "ghcr.io/x/y@sha256:" + "a" * 64])
        self.assertEqual((deadline, min_bps), (600.0, 20480))
        m = re.search(r"^albums_svc_pull_timeout_sec: (\d+)$", rtlib.read(self.DEFAULTS), re.M)
        self.assertEqual(int(m.group(1)), 600)

    def test_bounds_checked_in_runtime_too(self):
        # С --tags albums-runtime assert.yml не идёт: границы продублированы рядом с pull, иначе timeout 0 снял бы предел.
        text = rtlib.read(self.RUNTIME)
        i = text.index("albums_svc_pull_timeout_sec is integer")
        j = text.index("(с пределом времени)")
        self.assertLess(i, j)
        self.assertRegex(text, r"albums_svc_pull_timeout_sec >= 60")
        self.assertRegex(text, r"albums_svc_pull_timeout_sec <= 3600")

    def test_kill_exit_code_137_is_named_as_limit(self):
        # После --kill-after timeout отдаёт 137, а не 124.
        self.assertIn("in [124, 137]", rtlib.read(self.RUNTIME))


if __name__ == "__main__":
    rtlib.main()
