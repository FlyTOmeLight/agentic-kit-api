package table

import "gorm.io/gorm"

// Migrate registers the entire schema on the caller-owned database or transaction.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Agent{},
		&AgentVersion{},
		&Session{},
		&Run{},
		&Step{},
		&Event{},
		&Message{},
	)
}
