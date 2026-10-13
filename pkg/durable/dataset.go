package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
	sdk "github.com/FlyTOmeLight/agentic-kit-api/sdk/go/agenticv1"
)

func datasetFromRow(r table.Dataset) *sdk.Dataset {
	return &sdk.Dataset{
		ID:               r.ID,
		Name:             r.Name,
		Revision:         r.Revision,
		PublishedVersion: r.PublishedVersion,
		Cases:            r.Cases,
		UpdatedAt:        r.UpdatedAt.UTC(),
	}
}

func (s *Store) Datasets(ctx context.Context, owner, after string, limit int) ([]sdk.Dataset, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	var rows []table.Dataset
	if err := s.db.WithContext(ctx).
		Where("owner_id = ? AND id > ?", owner, after).
		Order("id").
		Limit(limit).
		Find(&rows).
		Error; err != nil {
		return nil, err
	}
	out := make([]sdk.Dataset, 0, len(rows))
	for _, r := range rows {
		out = append(out, *datasetFromRow(r))
	}
	return out, nil
}

func (s *Store) Dataset(ctx context.Context, owner, id string) (*sdk.Dataset, error) {
	var r table.Dataset
	if err := s.db.WithContext(ctx).Where("owner_id = ? AND id = ?", owner, id).Take(&r).Error; err != nil {
		return nil, recordError(err)
	}
	return datasetFromRow(r), nil
}

func (s *Store) SaveDataset(ctx context.Context, owner, id string, req sdk.SaveDatasetRequest) (*sdk.Dataset, error) {
	if owner == "" || strings.TrimSpace(id) == "" || len(id) > 128 || strings.TrimSpace(req.Name) == "" ||
		len(req.Name) > 200 ||
		req.Revision < 0 ||
		len(req.Cases) < 1 ||
		len(req.Cases) > 100 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range req.Cases {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Input) == "" || seen[c.ID] {
			return nil, ErrInvalid
		}
		seen[c.ID] = true
	}
	var row table.Dataset
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			"SELECT pg_advisory_xact_lock(hashtextextended(?,0))",
			"dataset:"+id,
		).Error; err != nil {
			return err
		}
		err := tx.Where("id = ?", id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if req.Revision != 0 {
				return ErrNotFound
			}
			row = table.Dataset{ID: id, OwnerID: owner, Name: req.Name, Revision: 1, Cases: req.Cases}
			return tx.Create(&row).Error
		}
		if err != nil {
			return err
		}
		if row.OwnerID != owner {
			return ErrNotFound
		}
		raw, _ := json.Marshal(req.Cases)
		existing, _ := json.Marshal(row.Cases)
		if row.Revision != req.Revision {
			if row.Revision == req.Revision+1 && row.Name == req.Name && string(raw) == string(existing) {
				return nil
			}
			return ErrConflict
		}
		return tx.Model(&row).
			Clauses(clause.Returning{}).
			Updates(map[string]any{"name": req.Name, "cases": string(raw), "revision": row.Revision + 1}).
			Error
	})
	if err != nil {
		return nil, err
	}
	return datasetFromRow(row), nil
}

func (s *Store) PublishDataset(ctx context.Context, owner, id string, revision int64) (*sdk.DatasetVersion, error) {
	var version table.DatasetVersion
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row table.Dataset
		if err := tx.Where("owner_id = ? AND id = ?", owner, id).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Take(&row).
			Error; err != nil {
			return recordError(err)
		}
		if row.Revision != revision {
			return ErrConflict
		}
		if row.PublishedVersion > 0 {
			if err := tx.Where("dataset_id = ? AND number = ?", id, row.PublishedVersion).
				Take(&version).
				Error; err != nil {
				return err
			}
			raw, _ := json.Marshal(row.Cases)
			old, _ := json.Marshal(version.Snapshot.Cases)
			if version.Snapshot.Name == row.Name && string(raw) == string(old) {
				return nil
			}
		}
		n := row.PublishedVersion + 1
		version = table.DatasetVersion{
			DatasetID: id,
			Number:    n,
			Snapshot: sdk.EvaluationDataset{
				ID:      id,
				Name:    row.Name,
				Version: strconv.FormatInt(n, 10),
				Cases:   row.Cases,
			},
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		return tx.Model(&row).Update("published_version", n).Error
	})
	if err != nil {
		return nil, err
	}
	return &sdk.DatasetVersion{
		DatasetID: id,
		Number:    version.Number,
		Snapshot:  version.Snapshot,
		CreatedAt: version.CreatedAt.UTC(),
	}, nil
}

func (s *Store) DatasetVersion(ctx context.Context, owner, id string, n int64) (*sdk.DatasetVersion, error) {
	if _, err := s.Dataset(ctx, owner, id); err != nil {
		return nil, err
	}
	var v table.DatasetVersion
	if err := s.db.WithContext(ctx).Where("dataset_id = ? AND number = ?", id, n).Take(&v).Error; err != nil {
		return nil, recordError(err)
	}
	return &sdk.DatasetVersion{DatasetID: id, Number: n, Snapshot: v.Snapshot, CreatedAt: v.CreatedAt.UTC()}, nil
}
