"""Conformance контракта /v1 через Caddy стенда с двумя токенами.

Сама проверка — Go (albumstest.Conformance из модуля контракта, deploy/tests/conformance/): ей нужен Go-клиент
контракта, переписывать её на Python значило бы подменить проверяемое. stand.sh запускает её в контейнере golang
по дайджесту после стадии main, пишет out/conformance.rc и out/conformance.log; здесь итог попадает в общий
счёт матрицы и проверяется на «не пропущена и не пуста».
"""

import os
import re

import pytest

import sa

BLOCKS = ["health", "auth", "ids", "foreign", "input", "pages", "publish", "password", "revoke", "lifecycle"]


def _masked(text: str) -> str:
    """Вывод Go-теста в сообщение pytest — без строк вида id или токена (≥ 22 знаков base64url)."""
    return re.sub(r"[A-Za-z0-9_-]{22,}", "<скрыто>", text)


def _read(name: str) -> str:
    with open(os.path.join(sa.OUT, name), encoding="utf-8", errors="replace") as f:
        return f.read()


@pytest.mark.stage("logs")
def test_conformance_via_caddy():
    assert _read("conformance.rc").strip() == "0", "Conformance через Caddy провален:\n" + _masked(_read("conformance.log"))[-2000:]
    log = _read("conformance.log")
    assert "--- PASS: TestConformanceViaCaddy (" in log and "--- SKIP" not in log and "FAIL" not in log
    for block in BLOCKS:
        assert f"--- PASS: TestConformanceViaCaddy/{block} " in log, f"блок {block} не прошёл или не запускался"
