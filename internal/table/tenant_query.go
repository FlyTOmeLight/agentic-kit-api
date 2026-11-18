package table

import (
	"context"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/access"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TenantQuery(ctx context.Context, db *gorm.DB) *gorm.DB {
	query := db.WithContext(ctx)
	scope, ok := access.FromContext(ctx)
	if !ok {
		return query
	}
	if scope.TenantID == "" {
		return query.Where("false")
	}
	return query.Where(clause.Eq{
		Column: clause.Column{Table: clause.CurrentTable, Name: "tenant_id"},
		Value:  scope.TenantID,
	})
}
