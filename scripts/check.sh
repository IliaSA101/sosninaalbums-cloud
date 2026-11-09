#!/usr/bin/env bash
# Все проверки одной командой (Linux): scripts/check.sh [--race] [--vuln]
#
# Без флагов — быстрый прогон: формат, vet, линтеры, тесты, web, размер файлов, shellcheck, приватное.
# --race — go test -race вместо обычного (нужен gcc/cgo). Перед коммитом пачки с конкурентным кодом.
# --vuln — govulncheck. Перед публикацией и после смены зависимостей.
#
# Правило проекта: ноль предупреждений. Шаг падает и на warning, не только на ошибке.
# Пока нет ни одного .go-файла, шаги Go пропускаются явно (SKIP), а не падают на «no packages».
# Функции шагов (gofmt_files, check_web_csp …) вызываются косвенно — через step "$@"; shellcheck
# этого не видит и считает их тела недостижимыми.
# shellcheck disable=SC2317
set -u

race=0 vuln=0
for a in "$@"; do
  case "$a" in
    --race | -race) race=1 ;;
    --vuln | -vuln) vuln=1 ;;
    *) echo "неизвестный флаг: $a (есть --race, --vuln)" >&2; exit 2 ;;
  esac
done

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root" || exit 1

if [ -t 1 ]; then red=$'\e[31m' green=$'\e[32m' yellow=$'\e[33m' off=$'\e[0m'; else red='' green='' yellow='' off=''; fi

if ! command -v go >/dev/null 2>&1; then
  echo "${red}FAIL go не найден в PATH${off}"
  exit 1
fi
# Проверяем тем же компилятором, которым собирается образ: иначе зелёный --vuln при более новом Go
# на машине ничего не говорит о том, что уедет на сервер.
if [ -f go.mod ]; then
  tc=$(awk '$1 == "toolchain" { print $2; exit }' go.mod)
  if [ -n "$tc" ]; then export GOTOOLCHAIN=$tc; fi
fi
gobin="$(go env GOPATH)/bin"
mkdir -p "$root/.gotmp"

failed=()
total_start=$(date +%s.%N)

# Запускает шаг, прячет вывод, печатает одну строку; при провале — хвост вывода.
# --empty-out: шаг провален, если вывод непустой (gofmt -l и наши собственные проверки).
step() {
  local name=$1 empty=0
  shift
  if [ "$1" = --empty-out ]; then empty=1; shift; fi
  local start out code ok=1
  start=$(date +%s.%N)
  out=$("$@" 2>&1)
  code=$?
  [ "$code" -ne 0 ] && ok=0
  [ "$empty" -eq 1 ] && [ -n "$(printf '%s' "$out" | tr -d '[:space:]')" ] && ok=0
  local time
  time=$(awk -v a="$start" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1fs", b - a }')
  if [ "$ok" -eq 1 ]; then
    echo "${green}OK   $name ($time)${off}"
  else
    echo "${red}FAIL $name ($time)${off}"
    printf '%s\n' "$out" | tail -n 30 | sed 's/^/     /'
    failed+=("$name")
  fi
}
skip() { echo "${yellow}SKIP $1${off}"; }
missing() {
  echo "${red}FAIL $1 не установлен ($2)${off}"
  failed+=("$1")
}

# Списком файлов, а не по каталогу: `gofmt -l .` заходит в .gotmp с обрывками кода без package.
# Рабочие копии агентов (.claude/worktrees) и данные стенда — не код этого дерева.
go_files() {
  find . -name '*.go' -type f -not -path './.gotmp/*' -not -path './.git/*' -not -path './vendor/*' \
    -not -path './.claude/*' -not -path './data/*'
}
# gofmt того же тулчейна, что go (toolchain в go.mod): другая версия форматирует иначе.
gofmt_bin="$(go env GOROOT)/bin/gofmt"
[ -x "$gofmt_bin" ] || gofmt_bin=gofmt
gofmt_files() { go_files | tr '\n' '\0' | xargs -0 -r "$gofmt_bin" -l; }

# Синтаксис ES-модулей просмотрщика. Именно через stdin и --input-type=module:
# `node --check файл.js` на модуле молча возвращает 0 даже для битого файла.
check_web_syntax() {
  find web \( -name '*.js' -o -name '*.mjs' \) -type f -not -path '*/vendor/*' | while IFS= read -r f; do
    local err
    if ! err=$(node --check --input-type=module <"$f" 2>&1); then
      echo "$f: $(printf '%s\n' "$err" | grep -v '^\s*$' | head -n 4 | paste -sd '|')"
    fi
  done
  return 0
}

# Совместимость со строгим CSP (default-src 'self', без unsafe-inline): inline-скрипты и обработчики
# в атрибутах он запрещает; innerHTML — XSS-сток (подписи разворотов приходят из сети);
# внешние ресурсы выдают клиенту адрес листалки третьей стороне через Referer и запросы.
# Исключений нет: ссылка «Исходный код» убрана в v0.1.2 (спека 005), а ссылка на сайт студии строится
# в листалке из view.json и в HTML не попадает.
check_web_csp() {
  local mask pattern what
  while IFS=$'\t' read -r mask pattern what; do
    find web -name "$mask" -type f -not -path '*/vendor/*' -print0 |
      xargs -0 -r grep -HnP -- "$pattern" | sed "s|:\([0-9]*\):.*|:\1 — $what|"
  done <<'EOF'
*.html	<script(?![^>]*\bsrc=)[^>]*>\s*\S	inline <script> — вынести в файл и подключить src=
*.html	\son[a-z]+\s*=	обработчик в атрибуте — нужен addEventListener
*.html	(src|href)\s*=\s*["']https?://	внешний ресурс — всё своё, с того же источника
*.html	<style[\s>]	inline <style> — вынести в .css (CSP без unsafe-inline)
*.js	\b(innerHTML|outerHTML|insertAdjacentHTML)\b	запись HTML строкой — нужен textContent или createElement
*.js	\b(eval|Function)\s*\(	исполнение строки — запрещено CSP
EOF
  return 0
}

shell_files() {
  { git -c core.quotePath=false ls-files --cached --others --exclude-standard -- '*.sh'; echo scripts/git-hooks/pre-commit; echo scripts/git-hooks/pre-push; } |
    sort -u | while IFS= read -r f; do [ -f "$f" ] && echo "$f"; done
}

if [ -n "$(go_files | head -n 1)" ]; then
  step 'gofmt' --empty-out gofmt_files
  step 'go vet' go vet ./...

  # golangci-lint ставится через go install — лежит в GOPATH/bin, которого может не быть в PATH.
  gcl=$(command -v golangci-lint || echo "$gobin/golangci-lint")
  if [ -x "$gcl" ]; then
    step 'golangci-lint' "$gcl" run ./...
  else
    missing golangci-lint 'go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest'
  fi

  if [ "$race" -eq 1 ]; then
    # CGO_ENABLED=1 только на этот шаг: сервис собирается без cgo.
    step 'go test -race' env CGO_ENABLED=1 go test -race ./...
  else
    step 'go test' go test ./...
  fi

  if [ "$vuln" -eq 1 ]; then
    gvc=$(command -v govulncheck || echo "$gobin/govulncheck")
    if [ -x "$gvc" ]; then
      step 'govulncheck' "$gvc" ./...
    else
      missing govulncheck 'go install golang.org/x/vuln/cmd/govulncheck@latest'
    fi
  fi
else
  skip 'шаги Go (пока нет ни одного .go-файла)'
fi

if [ -d web ]; then
  if command -v node >/dev/null 2>&1; then
    step 'синтаксис web' --empty-out check_web_syntax
    # Тесты листалки: колесо и тачпад, правило адреса сайта на общем наборе site_cases.json (третье из трёх мест).
    step 'тесты web' node --test 'web/viewer/*.test.mjs'
  else
    skip 'синтаксис web (node не найден)'
  fi
  step 'CSP web' --empty-out check_web_csp
fi

step 'размер файлов' node scripts/limits.mjs sizes

if command -v shellcheck >/dev/null 2>&1; then
  mapfile -t sh < <(shell_files)
  if [ "${#sh[@]}" -gt 0 ]; then step 'shellcheck' shellcheck "${sh[@]}"; fi
else
  missing shellcheck 'apt install shellcheck'
fi

# Раскатка (спека 002, design §12): проверки без сервера — OpenTofu (fmt, validate, test), ansible-lint, yamllint,
# syntax-check плеев и самопроверка инструментов в контейнерах по дайджесту; shellcheck по deploy/ — с машины
# (deploy/tests/provision/run.sh static). Чего ещё нет в дереве (deploy/tofu, deploy/ansible), run.sh пропускает
# с причиной, а как только оно появилось, шаг обязателен. Первый запуск качает образы (OpenTofu, Python) и пакеты
# pip, а `tofu init` ходит в реестр OpenTofu: без сети шаг падает (FAIL). Нет podman: локально — SKIP,
# в CI (CI=true) — FAIL, чтобы проверки не пропали молча.
if [ -f deploy/tests/provision/run.sh ]; then
  if command -v podman >/dev/null 2>&1; then
    step 'раскатка static' bash deploy/tests/provision/run.sh static
  elif [ "${CI:-}" = true ]; then
    missing podman 'нужен для deploy/tests/provision; есть в образе ubuntu-24.04'
  else
    skip 'раскатка static (podman не найден)'
  fi
fi

# Приметы приватного (домены, адреса, личные пути) по всему публичному дереву. Страж живёт в приватной
# оснастке; в чистом клоне публичного репозитория его нет — это не провал, а честный SKIP.
guard=.claude/hooks/public-guard.mjs
if [ -f "$guard" ]; then
  step 'приватное в публичном' node "$guard" --scan
else
  skip 'приватное в публичном (страж оснастки не найден)'
fi

if command -v gitleaks >/dev/null 2>&1; then
  step 'gitleaks' gitleaks dir --no-banner --redact .
else
  skip 'gitleaks (не установлен)'
fi

time=$(awk -v a="$total_start" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1fs", b - a }')
if [ "${#failed[@]}" -gt 0 ]; then
  list=$(printf '%s, ' "${failed[@]}")
  echo "${red}ИТОГО: упало - ${list%, } ($time)${off}"
  exit 1
fi
echo "${green}ИТОГО: всё чисто ($time)${off}"
exit 0
