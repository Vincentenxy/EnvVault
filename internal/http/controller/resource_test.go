package controller

import (
	"testing"

	"envVault/internal/store/postgres"
)

func TestPaginateSecrets(t *testing.T) {
	items := []postgres.Secret{
		{ID: "1"},
		{ID: "2"},
		{ID: "3"},
	}

	got, total := paginateSecrets(items, postgres.Pagination{PageNum: 2, PageSize: 2})
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("items = %#v, want only id 3", got)
	}
}

func TestPaginateSecretsOutOfRange(t *testing.T) {
	got, total := paginateSecrets([]postgres.Secret{{ID: "1"}}, postgres.Pagination{PageNum: 3, PageSize: 20})
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(got) != 0 {
		t.Fatalf("items length = %d, want 0", len(got))
	}
}
