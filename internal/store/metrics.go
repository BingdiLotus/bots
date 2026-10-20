package store

import (
	"context"

	"github.com/bingdilotus/bots/internal/domain"
)

func stateCounts(ctx context.Context, s *Store, column, table, tenant string) (map[string]int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+column+`,count(*) FROM `+table+` WHERE tenant_id=$1 GROUP BY `+column, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		out[state] = count
	}
	return out, rows.Err()
}

// Metrics returns current tenant ledger sizes for the read-only metrics
// endpoint. It intentionally exposes no message text, credentials or IDs.
func (s *Store) Metrics(ctx context.Context, tenant string) (domain.Metrics, error) {
	if tenant == "" {
		return domain.Metrics{}, domain.ErrInvalid
	}
	inbox, err := stateCounts(ctx, s, "state", "bots.inbox", tenant)
	if err != nil {
		return domain.Metrics{}, err
	}
	tasks, err := stateCounts(ctx, s, "status", "bots.tasks", tenant)
	if err != nil {
		return domain.Metrics{}, err
	}
	deliveries, err := stateCounts(ctx, s, "state", "bots.deliveries", tenant)
	if err != nil {
		return domain.Metrics{}, err
	}
	var active int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM bots.knowledge WHERE tenant_id=$1 AND active`, tenant).Scan(&active); err != nil {
		return domain.Metrics{}, err
	}
	return domain.Metrics{Inbox: inbox, Tasks: tasks, Deliveries: deliveries, KnowledgeActive: active}, nil
}
