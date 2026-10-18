package durable

import (
	"context"
	"errors"
	"time"

	frameworkdb "github.com/FlyTOmeLight/server-go/db"
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
	database, err := frameworkdb.NewDB(ctx, &frameworkdb.Config{
		Driver:       frameworkdb.DriverPostgreSQL,
		DSN:          dsn,
		MaxOpenConns: 20,
		MaxIdleConns: 5,
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: session(database), owned: true}, nil
}

// session applies the settings durable transactions rely on: silent SQL logs,
// UTC timestamps, PostgreSQL error translation, and the underlying dialector.
func session(database *frameworkdb.DB) *gorm.DB {
	handle := database.Session(&gorm.Session{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if dialect, ok := handle.Dialector.(*frameworkdb.AIBaseDialector); ok {
		handle.Config.Dialector = dialect.GetDialector()
	}
	handle.Config.TranslateError = true
	return handle
}

// New borrows a framework database. The caller owns the connection pool and
// must not close it through Store.
func New(database *frameworkdb.DB) (*Store, error) {
	if database == nil {
		return nil, errors.New("durable store requires a database")
	}
	return &Store{db: session(database)}, nil
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

// DB exposes the borrowed session for tests that inspect stored rows.
func (s *Store) DB() *gorm.DB { return s.db }
