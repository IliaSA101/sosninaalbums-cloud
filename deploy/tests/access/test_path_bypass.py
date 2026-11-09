"""Обход путей: `..`, кодированные точки и слэши, регистр, «//», разделители, нулевой байт. Любая запись пути к чужому
или к другому разделу (`/v1`) даёт 404, а не данные: каждый адрес проверяется при опубликованном альбоме и
действующем токене, чтобы обход выдал бы 200 (а не прятался за «не найдено»)."""

from __future__ import annotations

import json

import pytest

import checks
import sa
import sa_api

# Обязательные по design §10: /v1/%2e%2e/…, /V1/../…, /a/x/..%2f…, /v1/..%2f…
MANDATED = [
    "/v1/%2e%2e/v1/albums",
    "/v1/%2e%2e/a/{id}/manifest.json",
    "/V1/../v1/albums",
    "/V1/albums",
    "/V1/../a/{id}/manifest.json",
    "/a/x/..%2f..%2fv1/albums",
    "/a/x/..%2f..%2fa/{id}/manifest.json",
    "/a/x/..%2fv1%2falbums",
    "/v1/..%2fv1/albums",
    "/v1/..%2fa/{id}/manifest.json",
    "/v1/albums/..%2f..%2fa/{id}/manifest.json",
]
# Прочие записи пути к существующему альбому и к /v1: все — 404.
OTHER = [
    "/a/{id}/%2e%2e%2fmeta.json", "/a/{id}/./manifest.json", "/a/{id}/manifest.json/", "/a/{id}/manifest.json%00",
    "/a/{id}/%00", "/a;x/{id}/", "/a%2f{id}/", "/%61/{id}/", "/A/{id}/", "/a/{id}//manifest.json", "//a/{id}/manifest.json",
    "/a\\{id}\\manifest.json", "/a/{id}/manifest.json;x", "/a/%7Bid%7D/", "/a/{id}/..", "/a/{id}/../../v1/albums",
    "/a/{ID}/manifest.json", "/a/{ID}/", "/a/{id1}%2F{id2}/manifest.json", "/a/{id1}%2f{id2}/", "/a/{id1}%252F{id2}/",
    "/v1//albums", "/v1/./albums", "/v1%2falbums", "/v1/albums%2f", "/v1/albums%00", "/%76%31/albums", "/V1/health",
]


def test_control_token_and_album_are_valid(w):
    """Без этого проверки ниже были бы пустыми: токен годен, список виден, страница альбома открывается."""
    r = sa_api.OWNER1.call("GET", "/v1/albums")
    assert r.status == 200 and w.open.id in [x["id"] for x in json.loads(r.body)["albums"]]
    assert sa.get(w.open.path("manifest.json")).status == 200


@pytest.mark.parametrize("target", MANDATED + OTHER)
def test_no_path_bypass(w, target):
    i = w.open.id
    t = target.replace("{id}", i).replace("{ID}", i.upper()).replace("{id1}", i[:13]).replace("{id2}", i[13:])
    got = sa.raw(t, headers={"Authorization": f"Bearer {sa.TOKEN1}"})
    assert got.status == 404, f"{target}: {got.status}"
    assert i.encode() not in got.body and b'"albums"' not in got.body
    checks.assert_not_found(got, target)  # побайтно единый 404, не JSON-ответ API и не что-то иное
