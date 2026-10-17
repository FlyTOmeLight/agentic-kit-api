package durable

import (
	"context"
	"errors"
	"time"

	frameworkdb "github.com/FlyTOmeLight/server-go/db"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/table"
)

// Store executes durable transactions. Open owns its pool; New borrows a
// database whose lifetime is managed by the caller.
type Store struct {
	db    *gorm.DB
	owned bool
}

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
	return &Store{db: db, owned: true}, nil
}

// New uses an injected framework database; the caller owns its connection pool.
func New(database *frameworkdb.DB) (*Store, error) {
	if database == nil {
		return nil, errors.New("durable store requires a database")
	}
	handle := database.Session(
		&gorm.Session{
			Logger:  database.Logger.LogMode(logger.Silent),
			NowFunc: func() time.Time { return time.Now().UTC() },
		},
	)
	// Durable transactions use PostgreSQL error translation for uniqueness conflicts.
	// The framework's cross-database dialector does not expose ErrorTranslator.
	if dialect, ok := handle.Dialector.(*frameworkdb.AIBaseDialector); ok {
		handle.Config.Dialector = dialect.GetDialector()
	}
	handle.Config.TranslateError = true
	return &Store{db: handle}, nil
}

func (s *Store) Close() {
	if s.owned {
		pool, _ := s.db.DB()
		_ = pool.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	// Serialize concurrent schema initialization.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(7412101)").Error; err != nil {
			return err
		}
		// v0.1 stored a session label only, not an Agent/session/message graph.
		// Refuse to invent published configurations or silently hide old history.
		if tx.Migrator().HasTable(&table.Run{}) && !tx.Migrator().HasTable(&table.Session{}) {
			var count int64
			if err := tx.Model(&table.Run{}).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return errors.New(
					"database contains v0.1 runs without managed sessions; finish old tasks with v0.1 and use an empty database for v0.2",
				)
			}
		}
		return table.Migrate(tx)
	})
}

func readRun(query *gorm.DB) (*Run, error) {
	var r table.Run
	if err := query.Take(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return runFromRow(r), nil
}
