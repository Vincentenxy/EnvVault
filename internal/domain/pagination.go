package domain

// Pagination is the canonical request-side pagination shape shared by every
// list endpoint. Concrete repos must call Normalize() before computing
// offset/limit so that invalid values fall back to safe defaults.
type Pagination struct {
	PageNum  int
	PageSize int
}

// PaginatedResult is the response-side wrapper carrying items and the
// total count after filtering (so the client can render page indicators).
type PaginatedResult[T any] struct {
	Items []T
	Total int64
}

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Normalize clamps PageNum to >= 1 and PageSize to [1, maxPageSize] with a
// default of defaultPageSize when zero.
func (p Pagination) Normalize() Pagination {
	if p.PageNum < 1 {
		p.PageNum = 1
	}
	if p.PageSize <= 0 {
		p.PageSize = defaultPageSize
	}
	if p.PageSize > maxPageSize {
		p.PageSize = maxPageSize
	}
	return p
}

// Offset returns the number of rows to skip for the normalized page.
func (p Pagination) Offset() int {
	return (p.Normalize().PageNum - 1) * p.Normalize().PageSize
}

// Limit returns the clamped page size.
func (p Pagination) Limit() int {
	return p.Normalize().PageSize
}
