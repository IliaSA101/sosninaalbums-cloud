package album

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/IliaSA101/sosninaalbums-cloud/internal/logx"
	"github.com/IliaSA101/sosninaalbums-cloud/internal/store"
)

// SweepEvery — период уборки в работе (design §2, §3): срок истёк — 404 сразу, файлы уходят в пределах этого периода.
const SweepEvery = 5 * time.Minute

// SweepStats — что убрал проход.
type SweepStats struct {
	Expired   int  // удалено по сроку
	Abandoned int  // удалено: ни разу не опубликован дольше unpublished_ttl
	Drafts    int  // отброшено неопубликованных правок старше unpublished_ttl
	Held      bool // пауза часов: удаления по времени не выполнялись
}

// Sweep — один проход уборки: по сроку, брошенные выгрузки и правки (если пауза часов не стоит), затем
// мусор хранилища в работе (tmp старше 600 с, корзина и сироты под мьютексом альбома), затем last_seen.
// Каждый альбом перепроверяется под своим мьютексом: PATCH, продливший срок между выбором и удалением,
// выигрывает у уборки.
func (s *Service) Sweep() (SweepStats, error) {
	var st SweepStats
	var errs []error
	st.Held = s.clockHeld(s.now())
	if !st.Held {
		for _, id := range s.ids() {
			if err := s.sweepAlbum(id, &st); err != nil {
				errs = append(errs, err)
			}
			if st.Held { // пауза поставлена посреди прохода: дальше по времени не удаляем
				break
			}
		}
	}
	if _, err := s.st.SweepRunning(s); err != nil {
		errs = append(errs, err)
	}
	if !st.Held {
		// Часы могли уйти, пока шёл проход: touchLastSeen сам проверяет паузу под тем же замком, что и пишет.
		if err := s.touchLastSeen(s.now()); err != nil && !errors.Is(err, store.ErrNotDurable) {
			errs = append(errs, fmt.Errorf("last_seen: %w", err))
		}
	}
	if n := st.Expired + st.Abandoned + st.Drafts; n > 0 {
		s.log.Info("удалено по времени", slog.String(logx.KeyOp, "expire"), slog.Int("count", n))
	}
	return st, errors.Join(errs...)
}

// sweepAlbum решает судьбу одного альбома по времени под его мьютексом.
func (s *Service) sweepAlbum(id string, st *SweepStats) error {
	defer s.lock(id, true)()
	a := s.lookup(id)
	if a == nil {
		return nil
	}
	m := a.meta.Load()
	now := s.now()
	if s.clockHeld(now) { // часы проверяются перед каждым удалением, а не один раз на проход
		st.Held = true
		return nil
	}
	ttl := s.cfg.UnpublishedTTL
	switch {
	case !now.Before(m.Expires):
		if err := s.drop(a, m); err != nil {
			return err
		}
		st.Expired++
	case m.Published == nil && now.Sub(m.Created) > ttl:
		if err := s.drop(a, m); err != nil {
			return err
		}
		st.Abandoned++
	case m.Published != nil && m.Draft != nil && now.Sub(m.Draft.At) > ttl:
		// Неопубликованная правка отбрасывается через сутки от первой правки; опубликованная редакция остаётся.
		c := *m
		c.Draft, c.Updated = nil, now
		if err := s.commit(a, &c, false); err != nil {
			return err
		}
		s.prune(&c)
		st.Drafts++
	}
	return nil
}

// Run убирает каждые every, пока жив ctx. Паника в проходе не роняет процесс: в журнал без значения паники.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepSafe()
		}
	}
}

func (s *Service) sweepSafe() {
	defer func() {
		if recover() != nil {
			s.log.Error("паника в уборке", slog.String(logx.KeyOp, "sweep"))
		}
	}()
	if _, err := s.Sweep(); err != nil {
		s.log.Error("уборка не завершена", slog.String(logx.KeyOp, "sweep"), slog.Any(logx.KeyErr, err))
	}
}
