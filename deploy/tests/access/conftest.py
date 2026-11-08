"""Стадии прогона: stand.sh запускает pytest несколько раз, между запусками меняя стенд (повреждение файла,
остановка сервиса, сбор журналов). Тест без метки stage идёт в main; лишние стадии не пропускаются, а
отсеиваются — в итоге считаются только запущенные тесты."""

import pytest

import sa
import world


def pytest_collection_modifyitems(config, items):
    keep, drop = [], []
    for item in items:
        m = item.get_closest_marker("stage")
        (keep if (m.args[0] if m else "main") == sa.STAGE else drop).append(item)
    if drop:
        config.hook.pytest_deselected(items=drop)
    items[:] = keep


@pytest.fixture(scope="session")
def w() -> world.World:
    """Состояния альбомов матрицы, один набор на прогон."""
    return world.build()
