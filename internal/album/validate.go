package album

import (
	"fmt"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// checkLimits — предел числа страниц и суммы байт: 413 (К1: albums.Validate на превышение отдаёт
// ValidationError без признака предела, поэтому сервис считает сам и до Validate). Страница с байтами вне
// 1…page_bytes в сумму не входит — её отвергнет Validate (400), а не предел альбома.
func (s *Service) checkLimits(m *albums.Manifest) error {
	lim := s.cfg.Limits
	if len(m.Pages) > lim.Pages {
		return &TooLargeError{Reason: ReasonPages}
	}
	var total int64
	for i := range m.Pages {
		b := m.Pages[i].Bytes
		if b < 1 || b > lim.PageBytes {
			continue
		}
		if b > lim.AlbumBytes-total {
			return &TooLargeError{Reason: ReasonAlbumBytes}
		}
		total += b
	}
	return nil
}

// checkRules — правила сервиса сверх контракта: WebP выключен (001-6, К6); страницы с одним хэшем описаны
// одинаково — у них один файл на диске, и размеры, проверенные по нему, не должны расходиться с заявленными
// у другой страницы.
func (s *Service) checkRules(m *albums.Manifest) error {
	seen := map[string]albums.Page{}
	for i := range m.Pages {
		p := m.Pages[i]
		if p.Blank {
			continue
		}
		if p.Type == albums.TypeWebP && !s.cfg.AcceptWebP {
			return badManifest("pages[%d].type: image/webp не принимается этим сервисом", i)
		}
		if prev, ok := seen[p.SHA256]; ok && (prev.Type != p.Type || prev.Bytes != p.Bytes || prev.W != p.W || prev.H != p.H) {
			return badManifest("pages[%d]: одинаковый sha256 при разных type, bytes, w, h", i)
		}
		seen[p.SHA256] = p
	}
	return nil
}

// checkDisk — запас места (001-11): свободно − need ≥ disk_reserve_bytes, иначе 413.
func (s *Service) checkDisk(need int64) error {
	if need <= 0 { // новых байт нет (правка названия, срок): запас места не мешает
		return nil
	}
	free, err := s.free()
	if err != nil {
		return fmt.Errorf("свободное место: %w", err)
	}
	// need > 0 и reserve ≥ 0 (config.Validate): сложение в uint64 не переполняется на реальных размерах.
	if free < uint64(need)+uint64(max(s.cfg.DiskReserve, 0)) {
		return &TooLargeError{Reason: ReasonDisk}
	}
	return nil
}

// CheckSpace — проверка запаса места до чтения тела страницы (api): need — байты, которые будут записаны.
func (s *Service) CheckSpace(need int64) error { return s.checkDisk(need) }
