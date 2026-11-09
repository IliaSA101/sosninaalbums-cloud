package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
)

// versionLen — знаков хэша в каталоге: 8 hex = 32 бита, чтобы разные сборки не склеились в кэше браузера.
const versionLen = 8

// Version — хэш содержимого Assets (имена и байты): имя каталога /viewer/<Version>/. Меняется с любым байтом
// листалки, поэтому «Cache-Control: immutable» для файлов под ним честный, а HTML альбома, которого кэш не
// держит, всегда ссылается на актуальный каталог. Относительные импорты модулей остаются внутри каталога —
// переписывать их под хэши не нужно.
func Version() (string, error) {
	h := sha256.New()
	// WalkDir обходит в лексическом порядке — хэш не зависит от порядка файлов в памяти.
	err := fs.WalkDir(Assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(Assets, path)
		if err != nil {
			return err
		}
		// Имя и длина вперёд содержимого: перенос байтов между файлами хэш не сохранит.
		h.Write([]byte(path))
		h.Write([]byte{0})
		var n [8]byte
		for i, v := 0, uint64(len(b)); i < len(n); i, v = i+1, v>>8 {
			n[i] = byte(v)
		}
		h.Write(n[:])
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:versionLen], nil
}
