package view

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/access"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/album"
)

// login — POST /a/<id>/login (design §5.2). Альбом уже виден. Порядок:
//
//	нет пароля → просто перенаправление; нет адреса клиента → 503; длина тела до чтения → 413;
//	лимит попыток (проверка и учёт вместе, до сверки) → 429; сверка пароля за постоянное время →
//	303 с cookie или 401 с формой и без cookie.
//
// Пароль не попадает ни в журнал, ни в ответ, ни в адрес: форма — обычный POST.
func (h *Handler) login(c *call, acc album.Access) {
	id := c.id
	if acc.Password == nil { // входить не во что: ответ тот же, что у открытого альбома на GET
		c.redirect(http.StatusSeeOther, "/a/"+id+"/")
		return
	}
	ip, ok := h.clientIP(c.r)
	if !ok {
		c.code = "no_client_ip"
		c.err = errors.New("нет адреса клиента от прокси: вход закрыт")
		c.html(http.StatusServiceUnavailable, cacheNone, h.pagesOf(acc.Owner).login[""])
		return
	}
	pg := h.pagesOf(acc.Owner)
	// Предел до чтения: известная длина сверяется сразу, неизвестная (chunked) режется MaxBytesReader.
	if c.r.ContentLength > maxLoginBody {
		c.code = "too_large"
		c.w.Header().Set("Connection", "close") // непрочитанное тело не должно остаться в соединении
		c.html(http.StatusRequestEntityTooLarge, cacheNone, pg.login[msgTooBig])
		return
	}
	// MaxBytesReader получает исходный ResponseWriter: он по нему узнаёт «писатель net/http» и закрывает
	// соединение после 413 (обёртка statusWriter скрыла бы это).
	body, err := io.ReadAll(http.MaxBytesReader(c.w.ResponseWriter, c.r.Body, maxLoginBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.code = "too_large"
			c.html(http.StatusRequestEntityTooLarge, cacheNone, pg.login[msgTooBig])
			return
		}
		c.code = "bad_request" // обрыв чтения: клиент не дочитал форму, пароль не проверялся и не считался
		c.send(http.StatusBadRequest, "text/plain; charset=utf-8", h.sandboxCSP, cacheNone, nil)
		return
	}

	attempt, wait, ok := h.lim.Begin(session(id, acc), ip)
	if !ok {
		c.code = "login_limited"
		c.w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		c.html(http.StatusTooManyRequests, cacheNone, pg.login[msgLimited])
		return
	}
	// Форма без поля, непарный разбор, пустой пароль — тоже попытка: она уже засчитана.
	form, _ := url.ParseQuery(string(body)) // при ошибке разбора — то, что разобрано; остальное не нужно
	if h.pw.Verify(id, acc.Password.Salt, acc.Password.Hash, form.Get("password")) {
		if value, maxAge, ok := h.ck.Issue(session(id, acc), h.now(), acc.Expires); ok {
			attempt.Success()
			c.code = ""
			h.setSession(c, value, maxAge)
			c.redirect(http.StatusSeeOther, "/a/"+id+"/")
			return
		}
		// Верный пароль, но до конца срока меньше секунды: войти нельзя, а попытка остаётся засчитанной.
	}
	c.code = "login_failed"
	c.html(http.StatusUnauthorized, cacheNone, pg.login[msgWrong])
}

// setSession ставит cookie входа: Secure (браузер без него отвергнет __Secure-), HttpOnly (скрипту листалки
// она не нужна), SameSite=Lax (ссылка из мессенджера не просит пароль заново), путь — только этот альбом,
// без Domain (design §5.4).
func (h *Handler) setSession(c *call, value string, maxAge time.Duration) {
	http.SetCookie(c.w, &http.Cookie{
		Name: access.CookieName, Value: value, Path: "/a/" + c.id + "/",
		MaxAge: int(maxAge.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}
