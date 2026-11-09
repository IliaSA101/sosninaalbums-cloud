// Package imgcheck — проверка страницы по содержимому: сигнатура JPEG, линейная разметка файла от SOI до EOI
// (белый список маркеров, идентификаторы JFIF и ICC_PROFILE, между сканами только таблицы и COM, после EOI
// — не больше заполнителя) и размеры в пикселях (design §4, 001-25). Тип по Content-Type и по имени не
// проверяется: им верить нельзя. Картинка не декодируется — только разметка и заголовки, поэтому «бомба» из
// заявленных 20000×20000 ничего не стоит.
package imgcheck

import (
	"bufio"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
)

// ErrBadPage — страница негодна: не JPEG, лишние метаданные, размеры не те. Клиенту — 400 bad_page.
// Ошибки чтения самого источника (диск) под него не подпадают и возвращаются как есть.
var ErrBadPage = errors.New("страница негодна")

func bad(reason string) error { return fmt.Errorf("%w: %s", ErrBadPage, reason) }

// maxHeaderBytes — предел суммы сегментов вне энтропийных данных: у честной страницы это единицы килобайт
// (JFIF, ICC, таблицы), а гигабайт из подряд идущих COM читать незачем.
const maxHeaderBytes = 1 << 20

// maxTail — сколько байт заполнителя (только 0x00 и 0xFF) допускается после EOI. Некоторые кодировщики и
// передача по протоколам дописывают выравнивание; больше — уже не заполнитель, а спрятанные данные
// (полиглот: вторая картинка с Exif после EOI, как в MPO). 16 — с запасом на любое честное выравнивание.
const maxTail = 16

// maxScans — предел числа сканов (SOS) в файле (design §4, аудит Г1). Прогрессивный JPEG честных
// кодировщиков — 10…15 сканов; десятки тысяч сканов одного файла вешают браузер клиента на декодировании,
// хотя заголовки и размер файла в порядке. 64 — с большим запасом над любым кодировщиком.
const maxScans = 64

// Маркеры JPEG (ITU T.81, таблица B.1).
const (
	mSOI  = 0xD8
	mEOI  = 0xD9
	mSOS  = 0xDA
	mDQT  = 0xDB
	mDHT  = 0xC4
	mDRI  = 0xDD
	mAPP0 = 0xE0 // JFIF
	mAPP2 = 0xE2 // профиль ICC
	mAPPE = 0xEE // Adobe (APP14)
	mCOM  = 0xFE
)

// Идентификаторы сегментов APPn: без них APP0 и APP2 — не то, чем назвались. JFXX (миниатюра внутри
// APP0) и MPF (вторая картинка в APP2, формат MPO) не допускаются: в них едут чужие данные.
const (
	idJFIF  = "JFIF\x00"
	idICC   = "ICC_PROFILE\x00"
	idAdobe = "Adobe"
)

// headerPrefix — какой идентификатор обязан начинать данные сегмента-APP до первого SOS; для прочих
// допустимых маркеров — пусто. Возвращает ok=false для маркера вне белого списка.
func headerPrefix(m byte) (prefix string, ok bool) {
	switch m {
	case mAPP0:
		return idJFIF, true
	case mAPP2:
		return idICC, true
	case mAPPE:
		return idAdobe, true
	case mCOM, mDQT, mDHT, mDRI:
		return "", true
	}
	return "", isSOF(m)
}

// betweenScans — маркеры, допустимые между сканами (многопроходный JPEG): таблицы, интервал рестарта,
// комментарий. APPn там — способ спрятать метаданные после первого скана, SOFn — второй кадр.
func betweenScans(m byte) bool {
	switch m {
	case mDHT, mDQT, mDRI, mCOM:
		return true
	}
	return false
}

// isSOF — начало кадра: C0–C3, C5–C7, C9–CB, CD–CF (C4 — DHT, C8 — JPG, CC — DAC).
func isSOF(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != mDHT && m != 0xC8 && m != 0xCC
}

// Check проверяет файл страницы (ra — весь файл, size — его длина): своя линейная разметка от SOI до EOI
// (белый список маркеров и идентификаторов, сканы, отсутствие хвоста), затем разбор заголовка стандартным
// декодером — он знает, чего `image/jpeg` не умеет (арифметическое кодирование, 12 бит), и браузер клиента
// такое тоже не покажет. Размеры сверяются с заявленными в манифесте.
func Check(ra io.ReaderAt, size int64, w, h int) error {
	if err := scan(io.NewSectionReader(ra, 0, size), w, h); err != nil {
		return err
	}
	return decodable(io.NewSectionReader(ra, 0, size), w, h)
}

// scan проходит файл один раз, без возвратов: заголовки до первого SOS по белому списку, затем сканы
// (энтропийные данные пропускаются, между сканами — только таблицы и комментарии) до первого EOI; после EOI
// — не более maxTail байт заполнителя. Метаданные (Exif, XMP, IPTC, MPF) не проходят ни в одном месте файла.
func scan(r io.Reader, w, h int) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil {
		return eofToBad(err, "короче сигнатуры")
	}
	if soi[0] != 0xFF || soi[1] != mSOI {
		return bad("нет сигнатуры JPEG")
	}
	if b, err := br.Peek(1); err != nil || b[0] != 0xFF {
		return eofToBad(err, "после SOI нет маркера")
	}
	var st state
	scans := 0
	m, err := nextMarker(br)
	for {
		if err != nil {
			return err
		}
		switch m {
		case mEOI:
			if scans == 0 {
				return bad("EOI до первого скана")
			}
			if err := checkTail(br); err != nil {
				return err
			}
			if st.w != w || st.h != h || st.w == 0 || st.h == 0 {
				return bad("размеры не совпали с заявленными")
			}
			return nil
		case mSOS:
			if !st.haveSOF {
				return bad("данные без заголовка кадра")
			}
			if err := sosHeader(br); err != nil {
				return err
			}
			if scans++; scans > maxScans {
				return bad("сканов больше предела")
			}
			m, err = skipEntropy(br) // маркер, которым кончился скан, разбирает следующий круг
			continue
		}
		if err := segment(br, m, scans > 0, &st); err != nil {
			return err
		}
		m, err = nextMarker(br)
	}
}

// state — что нужно помнить между сегментами: кадр и сумма сегментов вне энтропийных данных.
type state struct {
	haveSOF bool
	w, h    int
	total   int
}

// segment разбирает один сегмент с длиной (всё, кроме SOI, EOI, SOS и RSTn): допустим ли маркер в этом месте
// (до первого скана — белый список с идентификаторами, между сканами — только таблицы и COM), кадр — один.
func segment(br *bufio.Reader, m byte, afterScan bool, st *state) error {
	if m == mSOI || (m >= 0xD0 && m <= 0xD7) || m == 0x01 {
		return bad("маркер без данных вне скана")
	}
	prefix, ok := "", betweenScans(m)
	if !afterScan {
		prefix, ok = headerPrefix(m)
	}
	if !ok {
		return bad(fmt.Sprintf("маркер FF%02X вне белого списка", m))
	}
	var lb [2]byte
	if _, err := io.ReadFull(br, lb[:]); err != nil {
		return eofToBad(err, "обрыв длины сегмента")
	}
	n := int(lb[0])<<8 | int(lb[1])
	if n < 2 {
		return bad("длина сегмента меньше 2")
	}
	n -= 2
	if st.total += n; st.total > maxHeaderBytes {
		return bad("заголовки длиннее предела")
	}
	if prefix != "" {
		if n < len(prefix) {
			return bad(fmt.Sprintf("сегмент FF%02X без идентификатора", m))
		}
		b, err := br.Peek(len(prefix))
		if err != nil {
			return eofToBad(err, "обрыв сегмента")
		}
		if string(b) != prefix {
			return bad(fmt.Sprintf("сегмент FF%02X с чужим идентификатором", m))
		}
	}
	if isSOF(m) {
		if st.haveSOF {
			return bad("второй кадр в файле")
		}
		var f [5]byte // точность, высота, ширина
		if n < len(f) {
			return bad("кадр короче заголовка")
		}
		if _, err := io.ReadFull(br, f[:]); err != nil {
			return eofToBad(err, "обрыв заголовка кадра")
		}
		st.h, st.w, st.haveSOF = int(f[1])<<8|int(f[2]), int(f[3])<<8|int(f[4]), true
		n -= len(f)
	}
	if _, err := br.Discard(n); err != nil {
		return eofToBad(err, "обрыв сегмента")
	}
	return nil
}

// sosHeader читает заголовок скана: длина 6+2·Ns, Ns от 1 до 4.
func sosHeader(br *bufio.Reader) error {
	var hd [3]byte
	if _, err := io.ReadFull(br, hd[:]); err != nil {
		return eofToBad(err, "обрыв заголовка скана")
	}
	n, ns := int(hd[0])<<8|int(hd[1]), int(hd[2])
	if ns < 1 || ns > 4 || n != 6+2*ns {
		return bad("заголовок скана негоден")
	}
	if _, err := br.Discard(n - 3); err != nil {
		return eofToBad(err, "обрыв заголовка скана")
	}
	return nil
}

// skipEntropy пропускает энтропийные данные скана и возвращает маркер, которым он кончился. Внутри данных
// FF00 (набивка) и RSTn — часть скана, FF FF — заполнитель перед маркером.
func skipEntropy(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, eofToBad(err, "обрыв скана до EOI")
		}
		if b != 0xFF {
			continue
		}
		for {
			c, err := br.ReadByte()
			if err != nil {
				return 0, eofToBad(err, "обрыв скана до EOI")
			}
			switch {
			case c == 0x00 || (c >= 0xD0 && c <= 0xD7):
			case c == 0xFF:
				continue
			default:
				return c, nil
			}
			break
		}
	}
}

// checkTail: после EOI допустимо не больше maxTail байт, и только 0x00 или 0xFF.
func checkTail(br *bufio.Reader) error {
	tail, err := io.ReadAll(io.LimitReader(br, maxTail+1))
	if err != nil {
		return fmt.Errorf("чтение страницы: %w", err)
	}
	if len(tail) > maxTail {
		return bad("данные после EOI")
	}
	for _, b := range tail {
		if b != 0x00 && b != 0xFF {
			return bad("данные после EOI")
		}
	}
	return nil
}

// nextMarker читает следующий маркер: FF, затем байт не 00 и не FF (лишние FF — заполнитель).
func nextMarker(br *bufio.Reader) (byte, error) {
	b, err := br.ReadByte()
	if err != nil {
		return 0, eofToBad(err, "обрыв до SOS")
	}
	if b != 0xFF {
		return 0, bad("ожидался маркер")
	}
	for {
		b, err = br.ReadByte()
		if err != nil {
			return 0, eofToBad(err, "обрыв до SOS")
		}
		if b == 0 {
			return 0, bad("недопустимый маркер FF00")
		}
		if b != 0xFF {
			return b, nil
		}
	}
}

// eofToBad: конец файла посреди заголовка — это негодная страница, прочие ошибки — сбой чтения.
func eofToBad(err error, reason string) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return bad(reason)
	}
	return fmt.Errorf("чтение страницы: %w", err)
}

func decodable(r io.Reader, w, h int) error {
	cfg, err := jpeg.DecodeConfig(r)
	if err != nil {
		var fe jpeg.FormatError
		var ue jpeg.UnsupportedError
		if errors.As(err, &fe) || errors.As(err, &ue) || errors.Is(err, io.ErrUnexpectedEOF) {
			return bad("заголовок не разбирается")
		}
		return fmt.Errorf("чтение страницы: %w", err)
	}
	if cfg.Width != w || cfg.Height != h {
		return bad("размеры не совпали с заявленными")
	}
	return nil
}
