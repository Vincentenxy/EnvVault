package postgres

type Pagination struct {
	PageNum  int
	PageSize int
}

type PaginatedResult[T any] struct {
	Items []T
	Total int64
}

func (p Pagination) Normalize() Pagination {
	if p.PageNum < 1 {
		p.PageNum = 1
	}
	if p.PageSize < 1 {
		p.PageSize = 20
	}
	if p.PageSize > 100 {
		p.PageSize = 100
	}
	return p
}

func (p Pagination) Limit() int {
	return p.Normalize().PageSize
}

func (p Pagination) Offset() int {
	p = p.Normalize()
	return (p.PageNum - 1) * p.PageSize
}
