package store

import (
	"context"
	"sync"
	"time"

	"github.com/bingdilotus/bots/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantLease makes the MVP's single active process an enforced deployment
// constraint. Its dedicated session must remain alive until the engine stops.
type TenantLease struct {
	mu   sync.Mutex
	conn *pgxpool.Conn
	key  string
}

func (s *Store) AcquireTenant(ctx context.Context, tenant string) (*TenantLease, error) {
	if tenant == "" {
		return nil, domain.ErrInvalid
	}
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	key := "bingdi-bots:" + tenant
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		conn.Release()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, domain.ErrBusy
	}
	return &TenantLease{conn: conn, key: key}, nil
}
func (l *TenantLease) Check(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return domain.ErrBusy
	}
	return l.conn.Conn().Ping(ctx)
}
func (l *TenantLease) Close(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return nil
	}
	conn := l.conn
	l.conn = nil
	var released bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, l.key).Scan(&released)
	if err != nil || !released {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Conn().Close(closeCtx)
		cancel()
		if err == nil {
			err = domain.ErrBusy
		}
	}
	conn.Release()
	return err
}
