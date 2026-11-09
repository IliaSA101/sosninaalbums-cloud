package imgcheck

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// solidJPEG — однотонный JPEG w×h, сгенерированный в тесте (никаких реальных фото).
func solidJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 120, 40, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seg — сегмент с маркером m и данными payload.
func seg(m byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, m, byte(n >> 8), byte(n)}, payload...)
}

// withSegments вставляет сегменты сразу после SOI.
func withSegments(jpg []byte, segs ...[]byte) []byte {
	out := append([]byte(nil), jpg[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

func check(b []byte, w, h int) error { return Check(bytes.NewReader(b), int64(len(b)), w, h) }

func TestCheckAccepts(t *testing.T) {
	base := solidJPEG(t, 64, 48)
	jfif := seg(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))
	icc := seg(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), make([]byte, 100)...))
	adobe := seg(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01"))
	com := seg(0xFE, []byte("Sosnina Albums, метка программы"))
	dri := seg(0xDD, []byte{0, 8})
	for name, b := range map[string][]byte{
		"как есть (кодировщик Go)": base,
		"JFIF": withSegments(base, jfif),
		"JFIF+ICC+COM (экспорт программы)": withSegments(base, jfif, com, icc),
		"APP14":      withSegments(base, adobe),
		"COM пустой": withSegments(base, seg(0xFE, nil)),
		"DRI":        withSegments(base, dri),
		"заполнитель FF перед маркером": withSegments(base, []byte{0xFF}, com),
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(b, 64, 48); err != nil {
				t.Fatalf("годная страница отвергнута: %v", err)
			}
		})
	}
}

func TestCheckRejects(t *testing.T) {
	base := solidJPEG(t, 64, 48)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewGray(image.Rect(0, 0, 64, 48))); err != nil {
		t.Fatal(err)
	}
	huge := withSegments(base)
	for i := 0; i < 20; i++ { // 20 × 65 КБ комментариев — больше предела заголовков
		huge = withSegments(huge, seg(0xFE, make([]byte, 65000)))
	}
	cases := []struct {
		name string
		data []byte
		w, h int
		want string
	}{
		{"Exif APP1", withSegments(base, seg(0xE1, []byte("Exif\x00\x00II*\x00"))), 64, 48, "FFE1"},
		{"XMP APP1", withSegments(base, seg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x/>"))), 64, 48, "FFE1"},
		{"IPTC APP13", withSegments(base, seg(0xED, []byte("Photoshop 3.0\x008BIM"))), 64, 48, "FFED"},
		{"APP11", withSegments(base, seg(0xEB, []byte("JP"))), 64, 48, "FFEB"},
		{"APP15", withSegments(base, seg(0xEF, []byte("x"))), 64, 48, "FFEF"},
		{"JFIF, а следом Exif", withSegments(base, seg(0xE0, []byte("JFIF\x00")), seg(0xE1, []byte("Exif\x00\x00"))), 64, 48, "FFE1"},
		{"PNG под видом JPEG", pngBuf.Bytes(), 64, 48, "сигнатуры"},
		{"RIFF/WebP", []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00"), 64, 48, "сигнатуры"},
		{"текст", []byte("<html><script>alert(1)</script></html>"), 64, 48, "сигнатуры"},
		{"пусто", nil, 64, 48, "сигнатуры"},
		{"один байт", []byte{0xFF}, 64, 48, "сигнатуры"},
		{"только SOI", []byte{0xFF, 0xD8}, 64, 48, "маркера"},
		{"SOI и мусор", []byte{0xFF, 0xD8, 0x00, 0x01, 0x02}, 64, 48, "маркера"},
		{"ширина не та", base, 65, 48, "размеры"},
		{"высота не та", base, 64, 49, "размеры"},
		{"обе поменяны местами", base, 48, 64, "размеры"},
		{"нулевые заявленные", base, 0, 0, "размеры"},
		{"сегмент короче 2", withSegments(base, []byte{0xFF, 0xFE, 0x00, 0x01}), 64, 48, "длина сегмента"},
		{"сегмент длиннее файла", append(append([]byte{0xFF, 0xD8}, 0xFF, 0xFE, 0xFF, 0xFF), make([]byte, 50)...), 64, 48, "обрыв"},
		{"FF00 вместо маркера", append([]byte{0xFF, 0xD8, 0xFF, 0x00}, base[2:]...), 64, 48, "FF00"},
		{"EOI до SOS", []byte{0xFF, 0xD8, 0xFF, 0xD9}, 64, 48, "EOI до первого скана"},
		{"RST до SOS", []byte{0xFF, 0xD8, 0xFF, 0xD0}, 64, 48, "вне скана"},
		{"SOS без кадра", []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02}, 64, 48, "без заголовка кадра"},
		{"заголовки длиннее предела", huge, 64, 48, "предела"},
		{"кадр короче заголовка", []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x04, 0x08, 0x00}, 64, 48, "короче"},
		{"арифметическое кодирование (SOF9)", bytes.Replace(base, []byte{0xFF, 0xC0}, []byte{0xFF, 0xC9}, 1), 64, 48, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.data, tc.w, tc.h)
			if !errors.Is(err, ErrBadPage) {
				t.Fatalf("ждали ErrBadPage, получили %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("причина %q не содержит %q", err, tc.want)
			}
		})
	}
}

func TestCheckSecondFrame(t *testing.T) {
	base := solidJPEG(t, 64, 48)
	sof := []byte{0xFF, 0xC0, 0, 11, 8, 0, 48, 0, 64, 1, 1, 0x11, 0}
	b := withSegments(base, sof) // свой кадр до настоящего: два SOF
	if err := check(b, 64, 48); !errors.Is(err, ErrBadPage) {
		t.Fatalf("два кадра приняты: %v", err)
	}
}

// Любое усечение годного файла до начала данных — ErrBadPage, а не паника и не другая ошибка.
func TestCheckTruncated(t *testing.T) {
	base := solidJPEG(t, 32, 16)
	sos := bytes.Index(base, []byte{0xFF, 0xDA})
	if sos < 0 {
		t.Fatal("в тестовом файле нет SOS")
	}
	for n := 0; n < sos; n++ {
		if err := check(base[:n], 32, 16); !errors.Is(err, ErrBadPage) {
			t.Fatalf("усечение до %d байт: %v", n, err)
		}
	}
}

type failAt struct{ err error }

func (f failAt) ReadAt([]byte, int64) (int, error) { return 0, f.err }

// Сбой самого источника (диск) — не «плохая страница»: клиент не виноват, ответ 5xx.
func TestCheckSourceErrorIsNotBadPage(t *testing.T) {
	boom := errors.New("диск отвалился")
	err := Check(failAt{boom}, 1000, 64, 48)
	if err == nil || errors.Is(err, ErrBadPage) || !errors.Is(err, boom) {
		t.Fatalf("ошибка источника: %v", err)
	}
}

func TestCheckLargeDeclaredSideIsCheap(t *testing.T) {
	// Заявленные 20000×20000 не декодируются: хватает заголовка.
	base := solidJPEG(t, 8, 8)
	b := bytes.Clone(base)
	i := bytes.Index(b, []byte{0xFF, 0xC0})
	b[i+5], b[i+6], b[i+7], b[i+8] = 0x4E, 0x20, 0x4E, 0x20 // 20000×20000
	if err := check(b, 20000, 20000); err != nil {
		t.Fatalf("заголовок большого кадра: %v", err)
	}
	if err := check(b, 8, 8); !errors.Is(err, ErrBadPage) {
		t.Fatalf("размеры должны не совпасть: %v", err)
	}
}

func FuzzCheck(f *testing.F) {
	base := solidJPEG(f, 16, 16)
	f.Add(base)
	f.Add(withSegments(base, seg(0xE1, []byte("Exif\x00\x00"))))
	f.Add(withSegments(base, seg(0xFE, []byte("c"))))
	f.Add([]byte{0xFF, 0xD8, 0xFF})
	f.Add([]byte("RIFF....WEBP"))
	f.Add(multiScan(nil, nil))
	f.Add(multiScan(seg(0xE1, []byte("Exif\x00\x00")), withSegments(base, seg(0xE1, []byte("Exif\x00\x00")))))
	f.Add(append(bytes.Clone(base), 0xFF, 0xD8, 0xFF, 0xE1))
	f.Fuzz(func(t *testing.T, b []byte) {
		err := check(b, 16, 16)
		if err != nil && !errors.Is(err, ErrBadPage) {
			t.Fatalf("не ErrBadPage на данных из памяти: %v", err)
		}
		if err == nil && !bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) {
			t.Fatal("принято без сигнатуры")
		}
	})
}
