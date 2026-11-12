package model

import (
	"database/sql/driver"

	"github.com/jackc/pgx/v5/pgtype"
)

// TextArray preserves PostgreSQL array values across GORM's database/sql boundary.
type TextArray []string

func (a TextArray) Value() (driver.Value, error) {
	if a == nil {
		a = TextArray{}
	}
	encoded, err := pgtype.NewMap().Encode(pgtype.TextArrayOID, pgtype.TextFormatCode, []string(a), nil)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (a *TextArray) Scan(value any) error {
	return pgtype.NewMap().SQLScanner((*[]string)(a)).Scan(value)
}
