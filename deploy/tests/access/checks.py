"""Общие проверки ответов: заголовки design §5.6 и единый 404."""

from __future__ import annotations

import functools

import sa

# Заголовки всех ответов /a/ (design §5.6). Каждый — ровно один раз: Caddy («?») добавляет только отсутствующие.
COMMON = {
    "x-robots-tag": "noindex, nofollow",
    "referrer-policy": "no-referrer",
    "x-content-type-options": "nosniff",
    "cross-origin-resource-policy": "same-origin",
    "x-frame-options": "DENY",
}
HTML_CSP = (
    f"default-src 'none'; script-src {sa.PUBLIC_BASE}/viewer/; style-src {sa.PUBLIC_BASE}/viewer/; "
    "img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)
SANDBOX_CSP = "default-src 'none'; sandbox"
HSTS = "max-age=31536000"
CACHE_NONE = "no-store"
CACHE_PAGE = "private, no-cache"
CACHE_IMAGE = "private, max-age=86400, immutable"


def check_common(s: sa.Snap, what: str = "") -> None:
    for name, want in COMMON.items():
        assert s.one(name) == want, f"{what}: {name} = {s.one(name)!r}, нужно {want!r}"
    assert s.get("server") is None, f"{what}: заголовок Server не нужен"
    assert s.one("strict-transport-security") == HSTS, f"{what}: HSTS = {s.one('strict-transport-security')!r}"


@functools.lru_cache(maxsize=1)
def not_found_ref() -> sa.Snap:
    """Эталон единого 404: ответ Caddy вне маршрутов (handle_errors) — те же байты и заголовки обязан давать сервис."""
    s = sa.get("/no-such-route")
    assert s.status == 404 and b"</html>" in s.body, "эталонный 404 Caddy не получен"
    return s


def diff_headers(a: sa.Snap, b: sa.Snap) -> str:
    da = {k: v for k, v in a.headers if k not in sa.VOLATILE}
    db = {k: v for k, v in b.headers if k not in sa.VOLATILE}
    out = []
    for k in sorted(set(da) | set(db)):
        if da.get(k) != db.get(k):
            out.append(f"{k}: {da.get(k)!r} != {db.get(k)!r}")
    if a.body != b.body:
        out.append(f"тело: {len(a.body)} Б != {len(b.body)} Б")
    return "; ".join(out)


def assert_not_found(s: sa.Snap, what: str = "") -> None:
    ref = not_found_ref()
    assert s.status == 404, f"{what}: статус {s.status}, нужно 404"
    assert s.comparable() == ref.comparable(), f"{what}: 404 не как у Caddy: {diff_headers(s, ref)}"
