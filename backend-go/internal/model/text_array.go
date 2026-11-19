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

/** Dimensions returns the PostgreSQL dimensions for the text array. */
func (a TextArray) Dimensions() []pgtype.ArrayDimension {
	return pgtype.FlatArray[string](a).Dimensions()
}

/** Index returns one text array element for PostgreSQL encoding. */
func (a TextArray) Index(i int) any {
	return pgtype.FlatArray[string](a).Index(i)
}

/** IndexType returns the PostgreSQL text array element type. */
func (a TextArray) IndexType() any {
	return pgtype.FlatArray[string](a).IndexType()
}

/** SetDimensions prepares the text array for PostgreSQL scanning. */
func (a *TextArray) SetDimensions(dimensions []pgtype.ArrayDimension) error {
	return (*pgtype.FlatArray[string])(a).SetDimensions(dimensions)
}

/** ScanIndex returns a scan target for one text array element. */
func (a TextArray) ScanIndex(i int) any {
	return pgtype.FlatArray[string](a).ScanIndex(i)
}

/** ScanIndexType returns the PostgreSQL text array scan target type. */
func (a TextArray) ScanIndexType() any {
	return pgtype.FlatArray[string](a).ScanIndexType()
}

// Int64Array preserves PostgreSQL bigint array values across GORM's database/sql boundary.
type Int64Array []int64

func (a Int64Array) Value() (driver.Value, error) {
	if a == nil {
		a = Int64Array{}
	}
	encoded, err := pgtype.NewMap().Encode(pgtype.Int8ArrayOID, pgtype.TextFormatCode, []int64(a), nil)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (a *Int64Array) Scan(value any) error {
	return pgtype.NewMap().SQLScanner((*[]int64)(a)).Scan(value)
}

/** Dimensions returns the PostgreSQL dimensions for the integer array. */
func (a Int64Array) Dimensions() []pgtype.ArrayDimension {
	return pgtype.FlatArray[int64](a).Dimensions()
}

/** Index returns one integer array element for PostgreSQL encoding. */
func (a Int64Array) Index(i int) any {
	return pgtype.FlatArray[int64](a).Index(i)
}

/** IndexType returns the PostgreSQL integer array element type. */
func (a Int64Array) IndexType() any {
	return pgtype.FlatArray[int64](a).IndexType()
}

/** SetDimensions prepares the integer array for PostgreSQL scanning. */
func (a *Int64Array) SetDimensions(dimensions []pgtype.ArrayDimension) error {
	return (*pgtype.FlatArray[int64])(a).SetDimensions(dimensions)
}

/** ScanIndex returns a scan target for one integer array element. */
func (a Int64Array) ScanIndex(i int) any {
	return pgtype.FlatArray[int64](a).ScanIndex(i)
}

/** ScanIndexType returns the PostgreSQL integer array scan target type. */
func (a Int64Array) ScanIndexType() any {
	return pgtype.FlatArray[int64](a).ScanIndexType()
}

// Float32Array encodes PostgreSQL real array parameters for database/sql calls.
type Float32Array []float32

func (a Float32Array) Value() (driver.Value, error) {
	if a == nil {
		a = Float32Array{}
	}
	encoded, err := pgtype.NewMap().Encode(pgtype.Float4ArrayOID, pgtype.TextFormatCode, []float32(a), nil)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

/** Dimensions returns the PostgreSQL dimensions for the float array. */
func (a Float32Array) Dimensions() []pgtype.ArrayDimension {
	return pgtype.FlatArray[float32](a).Dimensions()
}

/** Index returns one float array element for PostgreSQL encoding. */
func (a Float32Array) Index(i int) any {
	return pgtype.FlatArray[float32](a).Index(i)
}

/** IndexType returns the PostgreSQL float array element type. */
func (a Float32Array) IndexType() any {
	return pgtype.FlatArray[float32](a).IndexType()
}

/** SetDimensions prepares the float array for PostgreSQL scanning. */
func (a *Float32Array) SetDimensions(dimensions []pgtype.ArrayDimension) error {
	return (*pgtype.FlatArray[float32])(a).SetDimensions(dimensions)
}

/** ScanIndex returns a scan target for one float array element. */
func (a Float32Array) ScanIndex(i int) any {
	return pgtype.FlatArray[float32](a).ScanIndex(i)
}

/** ScanIndexType returns the PostgreSQL float array scan target type. */
func (a Float32Array) ScanIndexType() any {
	return pgtype.FlatArray[float32](a).ScanIndexType()
}
