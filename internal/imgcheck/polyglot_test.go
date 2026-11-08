package imgcheck

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Проверки аудита: метаданные и чужие данные не должны проходить ни до первого скана, ни между сканами,
// ни после EOI (MPO, полиглоты).

var (
	sofProg = seg(0xC2, []byte{8, 0, 48, 0, 64, 1, 1, 0x11, 0}) // кадр 64×48, прогрессивный, один компонент
	sosHdr  = []byte{0xFF, 0xDA, 0, 8, 1, 1, 0, 0, 0x3F, 0}     // заголовок скана с Ns=1
	dht     = seg(0xC4, append([]byte{0x00}, make([]byte, 16)...))
)

// multiScan собирает JPEG из двух сканов; between — сегменты между ними, tail — всё после EOI.
func multiScan(between []byte, tail []byte) []byte {
	b := []byte{0xFF, 0xD8}
	b = append(b, sofProg...)
	b = append(b, dht...)
	b = append(b, sosHdr...)
	b = append(b, 0x12, 0x34, 0xFF, 0x00, 0x56, 0xFF, 0xD0, 0x78, 0xFF, 0xFF) // набивка, RST0, заполнитель
	b = append(b, between...)
	b = append(b, dht...)
	b = append(b, sosHdr...)
	b = append(b, 0x9A, 0xBC)
	b = append(b, 0xFF, 0xD9)
	return append(b, tail...)
}

func TestCheckMultiScan(t *testing.T) {
	for name, b := range map[string][]byte{
		"два скана, FF00, RSTn и заполнитель внутри": multiScan(nil, nil),
		"между сканами DQT, DRI, COM":                multiScan(append(append(seg(0xDB, make([]byte, 65)), seg(0xDD, []byte{0, 8})...), seg(0xFE, []byte("c"))...), nil),
		"заполнитель из нулей и FF после EOI (16)":   multiScan(nil, bytes.Repeat([]byte{0x00, 0xFF}, 8)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := scan(bytes.NewReader(b), 64, 48); err != nil {
				t.Fatalf("годный многопроходный файл отвергнут: %v", err)
			}
		})
	}
}

func TestCheckPolyglotAndMetadata(t *testing.T) {
	base := solidJPEG(t, 64, 48)
	exif := seg(0xE1, []byte("Exif\x00\x00II*\x00"))
	second := withSegments(solidJPEG(t, 8, 8), exif) // вторая картинка с Exif — как в MPO
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"APP1 между сканами", multiScan(exif, nil), "FFE1"},
		{"APP13 между сканами", multiScan(seg(0xED, []byte("Photoshop 3.0\x00")), nil), "FFED"},
		{"APP2 MPF между сканами", multiScan(seg(0xE2, []byte("MPF\x00")), nil), "FFE2"},
		{"второй кадр между сканами", multiScan(seg(0xC0, []byte{8, 0, 1, 0, 1, 1, 1, 0x11, 0}), nil), "FFC0"},
		{"MPO: вторая картинка после EOI", append(bytes.Clone(base), second...), "после EOI"},
		{"хвост 17 нулей", append(bytes.Clone(base), make([]byte, 17)...), "после EOI"},
		{"хвост 16 нулей и байт", append(append(bytes.Clone(base), make([]byte, 16)...), 'x'), "после EOI"},
		{"хвост — текст", append(bytes.Clone(base), []byte("<?php system($_GET[1]); ?>")...), "после EOI"},
		{"хвост — SOI с заполнителем перед ним", append(append(bytes.Clone(base), 0xFF, 0xFF), 0xD8, 0xFF, 0xE1), "после EOI"},
		{"многопроходный с хвостом-картинкой", multiScan(nil, second), "после EOI"},
		{"APP0 без JFIF (JFXX)", withSegments(base, seg(0xE0, []byte("JFXX\x00\x10"))), "идентификатор"},
		{"APP0 короче идентификатора", withSegments(base, seg(0xE0, []byte("JF"))), "идентификатор"},
		{"APP0 с мусором", withSegments(base, seg(0xE0, []byte("PK\x03\x04hidden"))), "идентификатор"},
		{"APP2 MPF", withSegments(base, seg(0xE2, []byte("MPF\x00II*\x00"))), "идентификатор"},
		{"APP2 без ICC_PROFILE", withSegments(base, seg(0xE2, []byte("ICC_PROFILX\x00"))), "идентификатор"},
		{"APP2 без нуля после ICC_PROFILE", withSegments(base, seg(0xE2, []byte("ICC_PROFILE\x01"))), "идентификатор"},
		{"APP14 не Adobe", withSegments(base, seg(0xEE, []byte("Exif\x00\x00"))), "идентификатор"},
		{"обрыв внутри скана", base[:len(base)-10], "обрыв"},
		{"нет EOI (обрезан по последнему байту)", base[:len(base)-1], ""},
		{"SOS с неверной длиной", append(append([]byte{0xFF, 0xD8}, sofProg...), 0xFF, 0xDA, 0, 9, 1, 1, 0, 0, 0x3F, 0, 0), "скана"},
		{"SOS с Ns=5", append(append([]byte{0xFF, 0xD8}, sofProg...), 0xFF, 0xDA, 0, 16, 5), "скана"},
		{"EOI сразу после SOS без сегментов", append(append(append([]byte{0xFF, 0xD8}, sofProg...), sosHdr...), 0xFF, 0xD9, 'x'), "после EOI"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.data, 64, 48)
			if !errors.Is(err, ErrBadPage) {
				t.Fatalf("ждали ErrBadPage, получили %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("причина %q не содержит %q", err, tc.want)
			}
		})
	}
}

// Заполнитель из байт 0x00/0xFF после EOI честных файлов — допуск, а не уязвимость; ровно maxTail проходит.
func TestCheckTailLimit(t *testing.T) {
	base := solidJPEG(t, 64, 48)
	for n := 0; n <= maxTail; n++ {
		if err := check(append(bytes.Clone(base), make([]byte, n)...), 64, 48); err != nil {
			t.Fatalf("хвост %d нулей: %v", n, err)
		}
	}
	if err := check(append(bytes.Clone(base), bytes.Repeat([]byte{0xFF}, maxTail)...), 64, 48); err != nil {
		t.Fatalf("хвост из FF: %v", err)
	}
	if err := check(append(bytes.Clone(base), make([]byte, maxTail+1)...), 64, 48); !errors.Is(err, ErrBadPage) {
		t.Fatalf("хвост длиннее предела принят: %v", err)
	}
}

// Проход линейный: файл в 15 МБ из одних 0xFF/0x00 внутри скана разбирается без квадратичных возвратов.
func TestCheckLinearOnLargeScan(t *testing.T) {
	b := []byte{0xFF, 0xD8}
	b = append(b, sofProg...)
	b = append(b, sosHdr...)
	scanData := bytes.Repeat([]byte{0xFF, 0x00, 0xFF, 0xD3, 0xFF, 0xFF, 0x00}, 15<<20/7)
	b = append(b, scanData...)
	b = append(b, 0xFF, 0xD9)
	if err := scan(bytes.NewReader(b), 64, 48); err != nil {
		t.Fatalf("большой скан: %v", err)
	}
}
