package durable

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store owns one GORM database handle and its database/sql connection pool.
type Store struct{ db *gorm.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		TranslateError:       true,
		NowFunc:              func() time.Time { return time.Now().UTC() },
		// Domain errors are reported by callers; avoid logging prompts and tool results as SQL.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(20)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	if err = pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() { pool, _ := s.db.DB(); _ = pool.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	// Serialize schema changes across concurrently started migration commands.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7412101)").Error; err != nil {
			return err
		}
		// v0.1 stored a session label only, not an Agent/session/message graph.
		// Refuse to invent published configurations or silently hide old history.
		if tx.Migrator().HasTable(&runRecord{}) && !tx.Migrator().HasTable(&sessionRecord{}) {
			var count int64
			if err := tx.Model(&runRecord{}).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("database contains v0.1 runs without managed sessions; finish old tasks with v0.1 and use an empty database for v0.2")
			}
		}
		return tx.AutoMigrate(&agentRecord{}, &versionRecord{}, &sessionRecord{}, &runRecord{}, &stepRecord{}, &eventRecord{}, &messageRecord{})
	})
}

func readRun(query *gorm.DB) (*Run, error) {
	var r runRecord
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r.run(), nil
}
