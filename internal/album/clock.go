package album

import (
	"log/slog"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// Сторож часов (001-19). Неверные часы иначе удалили бы все альбомы разом (скачок вперёд) или оставили бы
// просроченные (назад). Пауза всех удалений по времени ставится, если:
//   - при старте now − last_seen > 30 суток или now < last_seen;
//   - в работе настенные часы разошлись с монотонными больше чем на 10 минут в любую сторону.
//
// Пауза пишется в LAYOUT.clock_hold и переживает перезапуск и auto-update; пока она стоит, last_seen не
// двигается и не идёт ни одно удаление по времени (срок, неопубликованный альбом, draft старше суток).
// Доступ по-прежнему решается по времени запроса. Снять паузу — `albums-svc -clock-ok -data <dir>` при
// остановленном сервисе.
const (
	maxStale = 30 * 24 * time.Hour
	maxDrift = 10 * time.Minute
)

// Причины паузы: фиксированный текст, без значений времени.
const (
	reasonStale = "last_seen старше 30 суток"
	reasonBack  = "часы показывают время раньше last_seen"
	reasonDrift = "настенные часы разошлись с монотонными больше чем на 10 минут"
)

// checkClockAtStart ставит паузу по last_seen (старт). Уже стоящая пауза остаётся.
func (s *Service) checkClockAtStart() {
	l := s.st.Layout()
	if l.ClockHold != nil {
		s.log.Warn("стоит пауза удалений по времени (сторож часов)", slog.String(logx.KeyOp, "clock"))
		return
	}
	now := s.now()
	switch {
	case now.Sub(l.LastSeen) > maxStale:
		s.setHold(reasonStale, now)
	case now.Before(l.LastSeen):
		s.setHold(reasonBack, now)
	}
}

// clockHeld проверяет часы и говорит, стоит ли пауза удалений по времени. Зовётся перед каждым удалением по
// времени: скачок между проходами и внутри прохода замечается до удаления, а не после.
func (s *Service) clockHeld(now time.Time) bool {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	return s.heldLocked(now)
}

// heldLocked — проверка паузы и дрейфа под clockMu.
func (s *Service) heldLocked(now time.Time) bool {
	if s.held || s.st.Layout().ClockHold != nil {
		return true
	}
	drift := now.Sub(s.w0) - (s.mono() - s.m0)
	if drift < 0 {
		drift = -drift
	}
	if drift > maxDrift {
		s.setHoldLocked(reasonDrift, now)
		return true
	}
	return false
}

func (s *Service) setHold(reason string, now time.Time) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	s.setHoldLocked(reason, now)
}

// setHoldLocked ставит паузу в памяти и пишет её в LAYOUT. Отказ записи не снимает паузу в этом процессе:
// хуже удалить лишнее, чем не записать паузу.
func (s *Service) setHoldLocked(reason string, now time.Time) {
	s.held = true
	l := s.st.Layout()
	l.ClockHold = &store.ClockHold{Reason: reason, At: now}
	if err := s.st.SetLayout(l); err != nil {
		s.log.Error("пауза часов не записана в LAYOUT", slog.String(logx.KeyOp, "clock"), slog.Any(logx.KeyErr, err))
	}
	s.log.Error("сторож часов: пауза всех удалений по времени", slog.String(logx.KeyOp, "clock"), slog.String(logx.KeyReason, reason))
}

// touchLastSeen двигает last_seen на now вне паузы (каждый проход уборки). Проверка паузы и запись — под одним
// clockMu: иначе пауза, поставленная между проверкой и записью, была бы затёрта на диске (SetLayout пишет
// весь LAYOUT). Пауза стоит — ничего не пишет.
func (s *Service) touchLastSeen(now time.Time) error {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	if s.heldLocked(now) {
		return nil
	}
	l := s.st.Layout()
	l.LastSeen = now
	return s.st.SetLayout(l)
}
