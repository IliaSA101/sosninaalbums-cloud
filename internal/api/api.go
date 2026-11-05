// Package api — HTTP /v1 по контракту модуля api/albums (design §4). Сервис публичный и принимает файлы из
// сети, поэтому порядок проверок в обработчике один и тот же: путь → токен → id по грамматике → предел размера
// до чтения тела → содержимое. Пакет не знает о диске: страницы и альбомы — через internal/album.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/names"
)

const (
	// maxJSON — предел тела JSON-запросов (design §4): манифест на 300 страниц — сотни килобайт.
	maxJSON = albums.MaxBody
	// uploadSlots — сколько страниц один владелец принимает одновременно (design §4).
	uploadSlots = 8
	// jsonPerOwner и jsonTotal — сколько JSON-запросов с телом разбирается одновременно: тело до 1 МиБ при
	// разборе стоит памяти, а контейнер ограничен по --memory.
	jsonPerOwner = 1
	jsonTotal    = 2
	// retryAfter — через сколько секунд клиенту повторить после 429.
	retryAfter = "5"
)

// Options — зависимости обработчика.
type Options struct {
	Log    *slog.Logger // журнал запросов (через logx); по умолчанию — никуда
	Tagger *logx.Tagger // метка альбома в журнале
}

// Handler — обработчик /v1/*. Любой путь вне /v1 и любой неканонический путь отвечают 404 (design §4).
type Handler struct {
	svc    *album.Service
	tokens *access.Tokens
	limits albums.Limits
	log    *slog.Logger
	tagger *logx.Tagger
	slots  *slots // загрузка страниц: 8 на владельца
	json   *slots // JSON-запросы с телом: 1 на владельца, 2 всего (память на разбор, аудит Г2)
}

// New — обработчик /v1. limits — то, что отдаёт GET /v1/health: те же числа, по которым сервис сам режет запросы.
func New(svc *album.Service, tokens *access.Tokens, limits albums.Limits, opt Options) *Handler {
	h := &Handler{svc: svc, tokens: tokens, limits: limits.OrDefault(), log: opt.Log, tagger: opt.Tagger, slots: newSlots(uploadSlots, 0), json: newSlots(jsonPerOwner, jsonTotal)}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	return h
}

// Маршруты (метка — для журнала и тестов; в журнал идёт шаблон, не путь).
const (
	routeHealth  = "GET /v1/health"
	routeList    = "GET /v1/albums"
	routeCreate  = "POST /v1/albums"
	routeUpdate  = "PUT /v1/albums/{id}"
	routePatch   = "PATCH /v1/albums/{id}"
	routeDelete  = "DELETE /v1/albums/{id}"
	routePage    = "PUT /v1/albums/{id}/pages/{n}"
	routePublish = "POST /v1/albums/{id}/publish"
	routeNone    = "-"
)

// match разбирает путь вручную, а не через ServeMux: тот перенаправляет (301) неканонические пути, а здесь они
// получают 404. known — путь существует хотя бы для одного метода; route пуст, если метод не подходит (ответ тот же — 404).
func match(method, path string) (route, id, n string, known bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segs) < 2 || segs[0] != "v1" {
		return "", "", "", false
	}
	for _, s := range segs {
		if s == "" { // завершающая «/» и пустой id — другой адрес, а не тот же маршрут
			return "", "", "", false
		}
	}
	var table map[string]string
	switch {
	case len(segs) == 2 && segs[1] == "health":
		table = map[string]string{http.MethodGet: routeHealth}
	case len(segs) == 2 && segs[1] == "albums":
		table = map[string]string{http.MethodGet: routeList, http.MethodPost: routeCreate}
	case len(segs) == 3 && segs[1] == "albums":
		table, id = map[string]string{http.MethodPut: routeUpdate, http.MethodPatch: routePatch, http.MethodDelete: routeDelete}, segs[2]
	case len(segs) == 4 && segs[1] == "albums" && segs[3] == "publish":
		table, id = map[string]string{http.MethodPost: routePublish}, segs[2]
	case len(segs) == 5 && segs[1] == "albums" && segs[3] == "pages":
		table, id, n = map[string]string{http.MethodPut: routePage}, segs[2], segs[4]
	default:
		return "", "", "", false
	}
	return table[method], id, n, true
}

// ServeHTTP — вход /v1: путь → маршрут → токен → id → обработчик.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := &call{h: h, w: &statusWriter{ResponseWriter: w}, r: r, route: routeNone, start: time.Now()}
	defer c.finish()

	path := r.URL.EscapedPath()
	if !names.CanonicalPath(path) { // %2e, «..», «//», регистр /V1: 404, а не 301 маршрутизатора
		c.notFound()
		return
	}
	route, id, n, known := match(r.Method, path)
	if !known || route == "" { // неверный метод на известном пути — тот же 404: кода 405 в контракте нет
		c.notFound()
		return
	}
	c.route = route
	if route == routeHealth { // без токена
		c.json(http.StatusOK, albums.Health{API: "v1", Limits: h.limits})
		return
	}
	m, err := h.tokens.Authenticate(r.Header.Get("Authorization"))
	if err != nil { // нет, чужой, плохой вид, не Bearer, токен в строке запроса — один и тот же 401
		c.w.Header().Set("WWW-Authenticate", "Bearer")
		c.fail(http.StatusUnauthorized, albums.CodeUnauthorized, "токен не принят")
		return
	}
	c.owner = m.Owner
	if route != routeList && route != routeCreate {
		if !names.ValidID(id) { // после токена: вне грамматики — тот же 404, что у несуществующего
			c.notFound()
			return
		}
		c.id = id
	}
	switch route {
	case routeList:
		c.json(http.StatusOK, albums.AlbumList{Albums: h.svc.List(c.owner)})
	case routeCreate:
		h.create(c)
	case routeUpdate:
		h.update(c)
	case routePatch:
		h.patch(c)
	case routeDelete:
		h.remove(c)
	case routePublish:
		h.publish(c)
	case routePage:
		h.putPage(c, n)
	}
}
