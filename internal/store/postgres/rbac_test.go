package postgres

import "testing"

func TestPaginationNormalize(t *testing.T) {
	tests := []struct {
		name     string
		input    Pagination
		expected Pagination
		offset   int
	}{
		{
			name:     "uses defaults",
			input:    Pagination{},
			expected: Pagination{PageNum: 1, PageSize: 20},
			offset:   0,
		},
		{
			name:     "caps page size",
			input:    Pagination{PageNum: 2, PageSize: 500},
			expected: Pagination{PageNum: 2, PageSize: 100},
			offset:   100,
		},
		{
			name:     "keeps valid values",
			input:    Pagination{PageNum: 3, PageSize: 25},
			expected: Pagination{PageNum: 3, PageSize: 25},
			offset:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.Normalize()
			if got != tt.expected {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.expected)
			}
			if offset := tt.input.Offset(); offset != tt.offset {
				t.Fatalf("Offset() = %d, want %d", offset, tt.offset)
			}
			if limit := tt.input.Limit(); limit != tt.expected.PageSize {
				t.Fatalf("Limit() = %d, want %d", limit, tt.expected.PageSize)
			}
		})
	}
}
