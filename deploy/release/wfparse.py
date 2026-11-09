"""Строгий разбор подмножества YAML для workflow GitHub — путь (а) из design 004 §2 (развилка 004-25).

Это не YAML-парсер общего вида, а белый список строк: всё, что не опознано, — отказ (Refusal). Вторую,
независимую картину строит PyYAML в workflow_guard.py; расхождение двух картин тоже отказ. Поэтому здесь
допускается только то подмножество, где парсеры YAML 1.1 и 1.2 не расходятся: блочные словари и списки с
отступом, простые и «двойные»/'одинарные' скаляры без экранирования, однострочный список скаляров в
квадратных скобках, `{}` только в `permissions` верхнего уровня и литерал `run: |`. Только stdlib.
"""

import re


class Refusal(Exception):
    """Файл вне допустимого подмножества (текст — для человека, по-русски)."""


class Scalar(str):
    """Строковое значение; comment — хвостовой комментарий строки (нужен для `uses: …@sha # тег`)."""

    comment = ""


# Выражения ${{ … }} — ровно из перечня, побайтово (design §2.а.4). Любое другое — отказ.
EXPRESSIONS = frozenset(
    {
        "github.ref",
        "github.token",
        "runner.temp",
        "inputs.version",
        "needs.checks.outputs.digest",
        "needs.build-image.outputs.digest",
        "jobs.build-image.outputs.digest",
        "steps.build.outputs.digest",
        "steps.attest2.outputs.bundle-path || steps.attest1.outputs.bundle-path",
        "secrets.GITHUB_TOKEN",
        "secrets.RELEASE_PACKAGES_TOKEN",
    }
)
EXPR_RE = re.compile(r"\$\{\{ ([^{}]*?) \}\}")
KEY_LINE = re.compile(r"^([A-Za-z0-9_][A-Za-z0-9_.-]*):(?: (.*))?$")
# Слова, которые вне комментария живут только в ключе канонической строки (design §2.а.3).
KEYWORDS = re.compile(
    r"\b(uses|runs-on|container|services|environment|secrets|vars|continue-on-error|shell)\b", re.IGNORECASE
)
BAD_START = set("&*!|>%@`?:,]}'\"#")
FLOW_ITEM = re.compile(r'^(?:"[^"\\,]*"|[^,\[\]{}&*!|>%@`\'"#:?\s][^,\[\]{}&*!|>%@`\'"#:?]*)$')


# Белый список символов - тот же, что ctl_pattern в deploy/ops/guard.sh и review.sh: печатный ASCII, кириллица и
# несколько знаков (§ © « » ° — … № → ≠ ≤ ≥). Табуляция, NBSP, типографские кавычки, короткое тире, галочки, невидимые и
# bidi-символы нигде, даже в комментариях: иначе review.sh откажет, а вид строки может обманывать.
EXTRA_CHARS = frozenset([0xA7, 0xA9, 0xAB, 0xB0, 0xBB, 0x2014, 0x2026, 0x2116, 0x2192, 0x2260, 0x2264, 0x2265])


def char_ok(ch):
    o = ord(ch)
    return 0x20 <= o <= 0x7E or 0x400 <= o <= 0x4FF or o in EXTRA_CHARS


def check_chars(text):
    """Только символы белого списка (design §2.а.1, 004-23); перевод строки - разделитель."""
    for n, line in enumerate(text.split("\n"), 1):
        for ch in line:
            if not char_ok(ch):
                raise Refusal(f"строка {n}: символ вне белого списка U+{ord(ch):04X}")
    if "\n" in text and not text.endswith("\n"):
        raise Refusal("файл не заканчивается переводом строки")


def mask(text, where):
    """Проверяет каждое ${{ … }} по перечню и заменяет его на «$X» для дальнейших проверок."""

    def one(m):
        if m.group(1) not in EXPRESSIONS:
            raise Refusal(f"{where}: выражение вне перечня: ${{{{ {m.group(1)} }}}}")
        return "$X"

    out = EXPR_RE.sub(one, text)
    if "${{" in out or "}}" in out:
        raise Refusal(f"{where}: выражение не в канонической форме «${{{{ имя }}}}»")
    return out


class Parser:
    def __init__(self, text):
        check_chars(text)
        self.lines = text.split("\n")
        if self.lines and self.lines[-1] == "":
            self.lines.pop()
        for n, line in enumerate(self.lines, 1):
            if line != line.rstrip(" "):
                raise Refusal(f"строка {n}: пробелы в конце строки")
            if line.startswith(("---", "...", "%")):
                raise Refusal(f"строка {n}: маркер документа или директива YAML")
        self.i = 0

    def fail(self, msg):
        raise Refusal(f"строка {self.i + 1}: {msg}")

    def _skip(self):
        while self.i < len(self.lines):
            s = self.lines[self.i].strip()
            if s == "" or s.startswith("#"):
                self.i += 1
            else:
                break

    def _peek(self):
        self._skip()
        if self.i >= len(self.lines):
            return None
        line = self.lines[self.i]
        body = line.lstrip(" ")
        return len(line) - len(body), body

    def parse(self):
        p = self._peek()
        if p is None or p[0] != 0 or p[1].startswith("-"):
            self.fail("ожидался словарь верхнего уровня без отступа")
        node = self._block(0, ())
        if self._peek() is not None:
            self.fail("лишнее после документа")
        return node

    def _block(self, indent, path):
        p = self._peek()
        if p[1] == "-" or p[1].startswith("- "):
            return self._seq(indent, path)
        return self._map(indent, path)

    def _map(self, indent, path):
        out = {}
        while True:
            p = self._peek()
            if p is None or p[0] < indent:
                return out
            if p[0] > indent:
                self.fail("отступ глубже ожидаемого (продолжение скаляра на следующей строке?)")
            m = KEY_LINE.match(p[1])
            if not m:
                self.fail("строка не вида «ключ: значение» (ключ в кавычках, поток, тег, якорь?)")
            key, rest = m.group(1), m.group(2)
            if key in out:
                self.fail(f"повтор ключа {key}")
            self.i += 1
            out[key] = self._value(key, rest, indent, path)

    def _value(self, key, rest, indent, path):
        if rest is None or rest == "" or rest.startswith("#"):
            p = self._peek()
            if p is not None and p[0] > indent:
                return self._block(p[0], path + (key,))
            return None
        if rest.startswith(" "):
            self.i -= 1
            self.fail("лишний пробел после двоеточия")
        if rest == "|":
            if key != "run":
                self.i -= 1
                self.fail("литерал «|» допустим только у run")
            return self._literal(indent)
        return self._inline(rest, key, path)

    def _literal(self, indent):
        lines, base = [], None
        while self.i < len(self.lines):
            line = self.lines[self.i]
            if line.strip() == "":
                lines.append("")
                self.i += 1
                continue
            ind = len(line) - len(line.lstrip(" "))
            if ind <= indent:
                break
            if base is None:
                base = ind
            if ind < base:
                self.fail("строка литерала левее первой")
            lines.append(line[base:])
            self.i += 1
        while lines and lines[-1] == "":
            lines.pop()
        if not lines:
            self.fail("пустой литерал run")
        text = "\n".join(lines) + "\n"
        if "${{" in text:
            self.fail("выражение ${{ }} в тексте run (design §2.а.4)")
        if KEYWORDS.search(text):
            self.fail("служебное слово в тексте run (design §2.а.3)")
        return Scalar(text)

    def _seq(self, indent, path):
        out = []
        while True:
            p = self._peek()
            if p is None or p[0] < indent:
                return out
            if p[0] > indent:
                self.fail("отступ глубже ожидаемого в списке")
            if not p[1].startswith("- "):
                self.fail("смесь списка и словаря")
            item = p[1][2:]
            if item.startswith(" ") or item == "":
                self.fail("элемент списка не вида «- значение»")
            if KEY_LINE.match(item):
                # Элемент-словарь: его ключи начинаются в колонке после «- ».
                self.lines[self.i] = " " * (indent + 2) + item
                out.append(self._map(indent + 2, path + ("[]",)))
            else:
                self.i += 1
                out.append(self._inline(item, None, path + ("[]",)))

    def _inline(self, v, key, path):
        where = f"строка {self.i}"
        if v == "{}":
            if key == "permissions" and path == ():
                return {}
            raise Refusal(f"{where}: «{{}}» допустимо только в permissions верхнего уровня")
        if v[0] == "[":
            return self._flow(v, where)
        comment = ""
        if v[0] in "\"'":
            pat = r'^"([^"\\]*)"(?: #(.*))?$' if v[0] == '"' else r"^'([^']*)'(?: #(.*))?$"
            m = re.match(pat, v)
            if not m:
                raise Refusal(f"{where}: кавычки со сложным содержимым (экранирование, продолжение)")
            val, comment = m.group(1), m.group(2) or ""
        else:
            if v[0] in BAD_START or v.startswith("- "):
                raise Refusal(f"{where}: значение начинается со служебного знака (якорь, ссылка, тег, поток, блок)")
            val = v
            if " #" in v:
                val, comment = v.split(" #", 1)
            if ": " in val or val.endswith(":"):
                raise Refusal(f"{where}: двоеточие внутри простого скаляра")
        if key == "name" and "${{" in val:
            # Имена заданий и шагов публикует API прогона: вычисленное выражение (токен) стало бы видно всем.
            raise Refusal(f"{where}: выражение ${{{{ }}}} в name недопустимо")
        if key == "run" and "${{" in val:
            raise Refusal(f"{where}: выражение ${{{{ }}}} в тексте run (design §2.а.4)")
        masked = mask(val, where)
        if "{" in masked or "}" in masked:
            raise Refusal(f"{where}: фигурные скобки вне ${{{{ }}}}")
        if KEYWORDS.search(masked):
            raise Refusal(f"{where}: служебное слово в значении (допустимо только как ключ канонической строки)")
        s = Scalar(val)
        s.comment = comment
        return s

    def _flow(self, v, where):
        m = re.match(r"^\[(.*)\](?: #.*)?$", v)
        if not m:
            raise Refusal(f"{where}: список в скобках не закрыт или вложен")
        body = m.group(1).strip()
        if body == "":
            return []
        out = []
        for item in body.split(","):
            item = item.strip()
            if not FLOW_ITEM.match(item):
                raise Refusal(f"{where}: элемент списка в скобках вне белого списка: {item!r}")
            val = item[1:-1] if item[0] == '"' else item
            if KEYWORDS.search(val):
                raise Refusal(f"{where}: служебное слово в списке")
            out.append(Scalar(val))
        return out


def parse(text):
    """Текст файла → дерево из dict / list / Scalar / None; иначе Refusal."""
    return Parser(text).parse()
