package durable

import "gorm.io/gorm"

// Tx borrows a caller transaction for accept and other cross-domain writes.
type Tx struct {
	db *gorm.DB
}

// BindTransaction attaches durable accept algorithms to an open transaction.
func BindTransaction(tx *gorm.DB) *Tx {
	return &Tx{db: tx}
}
