package imgcheck

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// manyScans собирает JPEG из n сканов подряд (кадр 64×48, прогрессивный).
func manyScans(n int) []byte {
	b := []byte{0xFF, 0xD8}
	b = append(b, sofProg...)
	b = append(b, dht...)
	for i := 0; i < n; i++ {
		b = append(b, sosHdr...)
		b = append(b, 0x12, 0x34)
		if i+1 < n {
			b = append(b, dht...) // между сканами — только таблицы
		}
	}
	return append(b, 0xFF, 0xD9)
}

// Бомба из сканов: ровно maxScans проходит, на единицу больше — отказ (аудит Г1).
func TestCheckScanLimit(t *testing.T) {
	if err := scan(bytes.NewReader(manyScans(maxScans)), 64, 48); err != nil {
		t.Fatalf("%d сканов отвергнуты: %v", maxScans, err)
	}
	for _, n := range []int{maxScans + 1, 5000} {
		err := scan(bytes.NewReader(manyScans(n)), 64, 48)
		if !errors.Is(err, ErrBadPage) || !strings.Contains(err.Error(), "сканов") {
			t.Fatalf("%d сканов: ждали отказ по числу сканов, получили %v", n, err)
		}
	}
}
