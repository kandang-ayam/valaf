package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valaf/valaf/internal/core"
)

// EvidenceRepo persists captured evidence. Implements core.EvidenceRepository.
type EvidenceRepo struct {
	pool *pgxpool.Pool
}

func NewEvidenceRepo(pool *pgxpool.Pool) *EvidenceRepo { return &EvidenceRepo{pool: pool} }

// SaveEvidence inserts all items for an incident in one transaction. result is
// stored only for ok items (the DB CHECK enforces this), and error only for the
// rest.
func (r *EvidenceRepo) SaveEvidence(ctx context.Context, incidentID string, items []core.EvidenceItem) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	for _, it := range items {
		request := it.Request
		if len(request) == 0 {
			request = []byte(`{}`)
		}

		var result any // NULL unless ok
		if it.Status == core.EvidenceOK {
			if len(it.Result) == 0 {
				result = []byte(`{}`)
			} else {
				result = []byte(it.Result)
			}
		}

		var errText any // NULL when empty
		if it.Error != "" {
			errText = it.Error
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO evidence_items (incident_id, collector, kind, request, result, status, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			incidentID, it.Collector, it.Kind, []byte(request), result, it.Status, errText,
		); err != nil {
			return fmt.Errorf("insert evidence (%s/%s): %w", it.Collector, it.Status, err)
		}
	}
	return tx.Commit(ctx)
}
