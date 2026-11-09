package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// rawPut шлёт PUT страницы по голому TCP: заголовки и, если body не nil, тело. Нужен, чтобы доказать, что
// ответ дан ДО чтения тела: тело не отправляется вовсе, а ответ приходит.
func (e *env) rawPut(path, tok string, hdr []string, body []byte) (status int, respBody string) {
	e.t.Helper()
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(context.Background(), "tcp", e.srv.Listener.Addr().String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		e.t.Fatal(err)
	}
	var req strings.Builder
	fmt.Fprintf(&req, "PUT %s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n", path, tok)
	for _, h := range hdr {
		req.WriteString(h + "\r\n")
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		if _, err := conn.Write(body); err != nil {
			e.t.Fatal(err)
		}
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		e.t.Fatalf("ответа до чтения тела нет: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// one — альбом с одной небелой страницей data (и белой) владельца p1; возвращает id.
func (e *env) one(data []byte, w, h int) albums.Created {
	e.t.Helper()
	m := manifestOf("Тест", data, nil)
	m.Pages[0] = pageOf(1, data, w, h)
	return e.create(e.tok1, m)
}

func TestPutPageLengthBeforeRead(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 10)
	cr := e.one(data, 16, 16)
	path := "/v1/albums/" + cr.ID + "/pages/1"
	// Expect: 100-continue: сервер присылает «100 Continue» только когда обработчик начинает читать тело. Ждём
	// сразу итоговый статус — значит, обработчик отказал, не прочитав ни байта (без Expect net/http перед ответом
	// сам дочитывает до 256 КБ неотправленного тела, и «до чтения» не доказать).
	jpegCT := "Content-Type: image/jpeg"
	cases := []struct {
		name   string
		hdr    []string
		status int
		code   string
	}{
		{"длина больше page_bytes", []string{jpegCT, fmt.Sprintf("Content-Length: %d", e.lim.PageBytes+1)}, 413, albums.CodeTooLarge},
		{"гигабайт заявлен", []string{jpegCT, "Content-Length: 1073741824"}, 413, albums.CodeTooLarge},
		{"длина не как в манифесте", []string{jpegCT, fmt.Sprintf("Content-Length: %d", len(data)+1)}, 409, albums.CodeHashMismatch},
		{"нулевая длина", []string{jpegCT, "Content-Length: 0"}, 409, albums.CodeHashMismatch},
		{"Content-Type не тот", []string{"Content-Type: text/html", fmt.Sprintf("Content-Length: %d", len(data))}, 400, albums.CodeBadPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := e.rawPut(path, e.tok1, append(tc.hdr, "Expect: 100-continue"), nil) // тело не отправлено
			if status != tc.status || !strings.Contains(body, `"code":"`+tc.code+`"`) {
				t.Fatalf("%d %s, ждали %d %s", status, body, tc.status, tc.code)
			}
		})
	}
	e.noParts()
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("отказы до чтения ничего не записали")
	}
}

func TestPutPageLengthMismatch409(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 20)
	cr := e.one(data, 16, 16)
	for name, body := range map[string][]byte{
		"короче": data[:len(data)-1], "длиннее": append(bytes.Clone(data), 0), "пусто": {},
	} {
		if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, body); r.status != 409 || r.errBody(t).Code != albums.CodeHashMismatch {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	e.noParts()
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("страница записана")
	}
}

func TestPutPageStreamLimit(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 30)
	cr := e.one(data, 16, 16)
	chunked := func(b []byte) io.Reader { return io.NopCloser(bytes.NewReader(b)) } // длина неизвестна → chunked
	for name, body := range map[string][]byte{
		"длиннее на байт":  append(bytes.Clone(data), 0),
		"в 50 раз длиннее": bytes.Repeat(data, 50),
		"короче":           data[:len(data)/2],
	} {
		r := e.do("PUT", "/v1/albums/"+cr.ID+"/pages/1", e.tok1, chunked(body), map[string]string{"Content-Type": albums.TypeJPEG})
		if r.status != 409 || r.errBody(t).Code != albums.CodeHashMismatch {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	e.noParts()
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("страница записана")
	}
}

func TestPutPageHash409(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 40)
	cr := e.one(data, 16, 16)
	other := bytes.Clone(data)
	other[len(other)/2] ^= 0xFF // та же длина и тот же вид, другой хэш
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, other); r.status != 409 || r.errBody(t).Code != albums.CodeHashMismatch {
		t.Fatalf("%d %s", r.status, r.body)
	}
	e.noParts()
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("страница с другим хэшем записана")
	}
}

// Содержимое проверяется по байтам, а не по Content-Type и не по заявлениям манифеста: хэш и длина сходятся,
// но файл не JPEG, с Exif, с чужими размерами — 400.
func TestPutPageSignature400(t *testing.T) {
	e := newEnv(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	for name, data := range map[string][]byte{
		"PNG под видом JPEG": png,
		"текст/HTML":         []byte("<html><script>alert(1)</script></html>"),
		"WebP под JPEG":      append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 40)...),
		"один байт":          {0xFF},
		"JPEG без EOI":       solid(t, 16, 16, 50)[:200],
	} {
		cr := e.one(data, 16, 16)
		r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data)
		if r.status != 400 || r.errBody(t).Code != albums.CodeBadPage {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
		if len(e.pageFiles(cr.ID)) != 0 {
			t.Fatalf("%s: файл записан", name)
		}
	}
	e.noParts()
}

func TestPutPageExif400(t *testing.T) {
	e := newEnv(t)
	exif := withSegments(solid(t, 16, 16, 60), seg(0xE1, []byte("Exif\x00\x00II*\x00GPS")))
	cr := e.one(exif, 16, 16)
	r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, exif)
	if r.status != 400 || r.errBody(t).Code != albums.CodeBadPage {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("страница с Exif записана")
	}
	e.noParts()
}

func TestPutPageTypeMismatch400(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 70)
	cr := e.one(data, 16, 16)
	for name, hdr := range map[string]map[string]string{
		"webp":          {"Content-Type": albums.TypeWebP},
		"text/html":     {"Content-Type": "text/html"},
		"без заголовка": {},
		"мусор":         {"Content-Type": ";;;"},
	} {
		r := e.do("PUT", "/v1/albums/"+cr.ID+"/pages/1", e.tok1, bytes.NewReader(data), hdr)
		if r.status != 400 || r.errBody(t).Code != albums.CodeBadPage {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	// Регистр и параметры допустимы: это тот же тип.
	r := e.do("PUT", "/v1/albums/"+cr.ID+"/pages/1", e.tok1, bytes.NewReader(data), map[string]string{"Content-Type": "Image/JPEG; charset=binary"})
	if r.status != 204 {
		t.Fatalf("Image/JPEG; charset: %d %s", r.status, r.body)
	}
}

func TestPutPageDims400(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 80)
	for name, wh := range map[string][2]int{"шире": {17, 16}, "выше": {16, 17}, "поменяны": {16, 15}, "крупнее": {1600, 1600}} {
		cr := e.one(data, wh[0], wh[1])
		r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data)
		if r.status != 400 || r.errBody(t).Code != albums.CodeBadPage {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	e.noParts()
}

func TestPutPageMarkers(t *testing.T) {
	e := newEnv(t)
	base := solid(t, 16, 16, 90)
	jfif := seg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
	com := seg(0xFE, []byte("Sosnina Albums"))
	icc := seg(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), make([]byte, 64)...))
	for name, tc := range map[string]struct {
		data []byte
		ok   bool
	}{
		"COM":              {withSegments(base, com), true},
		"APP2 (ICC)":       {withSegments(base, icc), true},
		"JFIF+COM+ICC":     {withSegments(base, jfif, com, icc), true},
		"APP13 (IPTC)":     {withSegments(base, seg(0xED, []byte("Photoshop 3.0\x008BIM"))), false},
		"APP1 (XMP)":       {withSegments(base, seg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00"))), false},
		"APP2 без профиля": {withSegments(base, seg(0xE2, []byte("MPF\x00"))), false},
		"хвост после EOI":  {append(bytes.Clone(base), []byte("<?php evil ?>")...), false},
	} {
		cr := e.one(tc.data, 16, 16)
		r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, tc.data)
		switch {
		case tc.ok && r.status != 204, !tc.ok && (r.status != 400 || r.errBody(t).Code != albums.CodeBadPage):
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
	}
	e.noParts()
}

func TestPutPageNoLength(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 100)
	cr := e.one(data, 16, 16)
	r := e.do("PUT", "/v1/albums/"+cr.ID+"/pages/1", e.tok1, io.NopCloser(bytes.NewReader(data)), map[string]string{"Content-Type": albums.TypeJPEG})
	if r.status != 204 {
		t.Fatalf("chunked без Content-Length: %d %s", r.status, r.body)
	}
	if len(e.pageFiles(cr.ID)) != 1 {
		t.Fatalf("страница не записана: %v", e.pageFiles(cr.ID))
	}
}

func TestPutPageBlank400(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 110)
	cr := e.one(data, 16, 16) // страница 2 — белая
	r := e.putPage(e.tok1, cr.ID, 2, albums.TypeJPEG, data)
	if r.status != 400 || r.errBody(t).Code != albums.CodeBadPage {
		t.Fatalf("белая страница: %d %s", r.status, r.body)
	}
	for _, n := range []string{"0", "3", "01", "-1", "1e0", "abc", "99999999999", "%31"} {
		r := e.do("PUT", "/v1/albums/"+cr.ID+"/pages/"+n, e.tok1, bytes.NewReader(data), map[string]string{"Content-Type": albums.TypeJPEG})
		if r.status != 400 && r.status != 404 {
			t.Fatalf("номер %q: %d %s", n, r.status, r.body)
		}
		if n != "%31" && (r.status != 400 || r.errBody(t).Code != albums.CodeBadPage) {
			t.Fatalf("номер %q: %d %s", n, r.status, r.body)
		}
	}
	if len(e.pageFiles(cr.ID)) != 0 {
		t.Fatal("отказы ничего не записали")
	}
}

func TestPutPageIdempotent(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 120)
	cr := e.one(data, 16, 16)
	for i := 0; i < 3; i++ {
		if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data); r.status != 204 {
			t.Fatalf("PUT #%d: %d %s", i, r.status, r.body)
		}
	}
	files := e.pageFiles(cr.ID)
	if len(files) != 1 {
		t.Fatalf("после трёх PUT файлов: %v", files)
	}
	if r := e.json("POST", "/v1/albums/"+cr.ID+"/publish", e.tok1, struct{}{}); r.status != 200 {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data); r.status != 204 {
		t.Fatalf("PUT опубликованной страницы: %d %s", r.status, r.body)
	}
	if got := e.pageFiles(cr.ID); len(got) != 1 || got[0] != files[0] {
		t.Fatalf("после publish и повторного PUT: %v", got)
	}
	e.noParts()
}

// Девять одновременных загрузок одного владельца: девятая — 429 с Retry-After; у другого владельца слоты свои.
func TestPutPageBusy429(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 130)
	cr := e.one(data, 16, 16)
	other := e.create(e.tok2, manifestOf("Другой", data, nil))
	path := e.srv.URL + "/v1/albums/" + cr.ID + "/pages/1"

	type up struct {
		w    *io.PipeWriter
		done chan *resp
	}
	var ups []up
	waitSum := slotWaiter(t, e.h.slots)
	// Провал теста не должен оставлять трубы открытыми: httptest.Server.Close в очистке ждал бы их вечно.
	t.Cleanup(func() {
		for _, u := range ups {
			_ = u.w.CloseWithError(io.ErrUnexpectedEOF) // повторное закрытие трубы безвредно
		}
	})
	for i := 0; i < uploadSlots; i++ {
		pr, pw := io.Pipe()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, path, pr)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("Authorization", "Bearer "+e.tok1)
		req.Header.Set("Content-Type", albums.TypeJPEG)
		done := make(chan *resp, 1)
		go func() {
			res, err := e.srv.Client().Do(req)
			if err != nil {
				done <- &resp{status: -1}
				return
			}
			defer func() { _ = res.Body.Close() }()
			b, _ := io.ReadAll(res.Body)
			done <- &resp{status: res.StatusCode, body: b}
		}()
		ups = append(ups, up{w: pw, done: done})
		if _, err := pw.Write(data[:1]); err != nil {
			t.Fatalf("загрузка %d: %v", i, err)
		}
		waitSum(i + 1) // запись вернулась, когда байт принял транспорт клиента; слот занимает сервер — ждём его
	}
	r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data)
	if r.status != 429 || r.errBody(t).Code != albums.CodeRateLimited || r.hdr.Get("Retry-After") != "5" {
		t.Fatalf("девятая загрузка: %d %s, Retry-After %q", r.status, r.body, r.hdr.Get("Retry-After"))
	}
	if r := e.putPage(e.tok2, other.ID, 1, albums.TypeJPEG, data); r.status != 204 {
		t.Fatalf("у другого владельца слоты свои: %d %s", r.status, r.body)
	}
	// Первая загрузка завершается — слот свободен.
	if _, err := ups[0].w.Write(data[1:]); err != nil {
		t.Fatal(err)
	}
	if got := <-ups[0].done; got.status != 204 {
		t.Fatalf("первая загрузка: %d %s", got.status, got.body)
	}
	waitSum(uploadSlots - 1) // ответ уходит раньше, чем обработчик вернёт слот
	if r := e.putPage(e.tok1, cr.ID, 1, albums.TypeJPEG, data); r.status != 204 {
		t.Fatalf("после освобождения слота: %d %s", r.status, r.body)
	}
	for _, u := range ups[1:] {
		_ = u.w.CloseWithError(io.ErrUnexpectedEOF) // остальные обрываются; сервер отвечает и чистит .part
		<-u.done
	}
	e.noPartsEventually()
}

// noPartsEventually — обработчики оборванных загрузок завершаются после ответа клиенту: ждём, пока они уберут
// .part (ограниченно, по опросу каталога).
func (e *env) noPartsEventually() {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ents, _ := readDirNames(e.dir + "/tmp")
		if len(ents) == 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("в tmp/ остались файлы: %v", ents)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Альбом удалён, пока читалось тело: финал под мьютексом даёт 404, .part убран, каталог не воскрес.
func TestPutPageBodyThenDelete(t *testing.T) {
	e := newEnv(t)
	data := solid(t, 16, 16, 140)
	cr := e.one(data, 16, 16)
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, e.srv.URL+"/v1/albums/"+cr.ID+"/pages/1", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Authorization", "Bearer "+e.tok1)
	req.Header.Set("Content-Type", albums.TypeJPEG)
	waitSum := slotWaiter(t, e.h.slots)
	t.Cleanup(func() { _ = pw.CloseWithError(io.ErrUnexpectedEOF) }) // провал теста не должен вешать Server.Close
	var wg sync.WaitGroup
	var got *resp
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := e.srv.Client().Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		got = &resp{status: res.StatusCode, body: b}
	}()
	if _, err := pw.Write(data[:len(data)/2]); err != nil {
		t.Fatal(err)
	}
	waitSum(1) // обработчик занял слот, найдя альбом; иначе DELETE мог бы опередить его и дать 404 до чтения
	if r := e.do("DELETE", "/v1/albums/"+cr.ID, e.tok1, nil, nil); r.status != 204 {
		t.Fatalf("DELETE: %d %s", r.status, r.body)
	}
	if _, err := pw.Write(data[len(data)/2:]); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	wg.Wait()
	if got == nil || got.status != 404 || got.errBody(t).Code != albums.CodeNotFound {
		t.Fatalf("PUT удалённого: %+v", got)
	}
	e.noParts()
	if ents, _ := readDirNames(e.dir + "/owners/" + pOne + "/albums"); len(ents) != 0 {
		t.Fatalf("каталог удалённого альбома воскрес: %v", ents)
	}
}
