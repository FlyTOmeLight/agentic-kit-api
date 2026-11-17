package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/memory"
)

const (
	// ResumeBatch bounds one background pass. Failed operations move behind
	// untouched ones through updated_at, so a bounded batch still makes
	// progress across repeated passes.
	ResumeBatch = 20

	// resumeBudget bounds one operation's remote calls and local activation.
	resumeBudget = 90 * time.Second
)

// ResumePending retries committed Pending content operations in the
// background. Each operation reuses the existing claim, archive and
// activation path, so it can race safely with the request that created it and
// with other API instances. Errors are aggregated per pass: one failed
// operation must not stop the remaining candidates.
func (s *Service) ResumePending(ctx context.Context, limit int) (int, error) {
	if s.archive == nil || limit < 1 {
		return 0, nil
	}
	var rows []table.MemoryOperation
	if err := s.db.WithContext(ctx).
		Where(
			"status = ? AND op_type IN ? AND (archive_until IS NULL OR archive_until <= clock_timestamp())",
			string(memory.OperationPending),
			[]string{
				string(memory.OperationCreate),
				string(memory.OperationUpdate),
				string(memory.OperationRestore),
			},
		).
		Order("updated_at, id").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return 0, mapError(err)
	}
	resumed := 0
	var errs []error
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return resumed, errors.Join(append(errs, err)...)
		}
		op := rows[i].ToDomain()
		doc, err := operationDocument(op)
		if err != nil {
			errs = append(errs, fmt.Errorf("resume memory operation %s: invalid document: %w", op.ID, err))
			if touchErr := s.deferPendingOperation(ctx, op.ID); touchErr != nil {
				errs = append(
					errs,
					fmt.Errorf(
						"resume memory operation %s: defer invalid receipt: %w",
						op.ID,
						touchErr,
					),
				)
			}
			continue
		}
		opCtx, cancel := context.WithTimeout(ctx, resumeBudget)
		archived, err := s.Archive(opCtx, op)
		if !archived {
			cancel()
			if err == nil {
				err = errors.New("pending operation has no archive binding")
				if touchErr := s.deferPendingOperation(ctx, op.ID); touchErr != nil {
					errs = append(
						errs,
						fmt.Errorf(
							"resume memory operation %s: defer unbound receipt: %w",
							op.ID,
							touchErr,
						),
					)
				}
			}
			if err != nil && !errors.Is(err, memory.ErrPending) {
				errs = append(errs, fmt.Errorf("resume memory operation %s: %w", op.ID, err))
			}
			continue
		}
		archiveErr := err
		_, err = s.activate(opCtx, op, doc)
		cancel()
		if archiveErr != nil {
			errs = append(
				errs,
				fmt.Errorf("resume memory operation %s: release archive claim: %w", op.ID, archiveErr),
			)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("resume memory operation %s: %w", op.ID, err))
			continue
		}
		resumed++
	}
	return resumed, errors.Join(errs...)
}

func (s *Service) deferPendingOperation(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).
		Model(&table.MemoryOperation{}).
		Where("id = ? AND status = ?", id, string(memory.OperationPending)).
		UpdateColumn("updated_at", gorm.Expr("clock_timestamp()")).
		Error
}
