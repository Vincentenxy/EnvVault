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

func TestCodePattern(t *testing.T) {
	valid := []string{"org-a", "project1", "groups-secrets"}
	for _, value := range valid {
		if !codePattern.MatchString(value) {
			t.Fatalf("codePattern rejected %q", value)
		}
	}

	invalid := []string{"", "Org-A", "-org", "org-", "org--a", "org_a", "组织"}
	for _, value := range invalid {
		if codePattern.MatchString(value) {
			t.Fatalf("codePattern accepted %q", value)
		}
	}
}

func TestSecretKeyPattern(t *testing.T) {
	valid := []string{"DATABASE_URL", "A1", "REDIS_PASSWORD"}
	for _, value := range valid {
		if !secretKeyPattern.MatchString(value) {
			t.Fatalf("secretKeyPattern rejected %q", value)
		}
	}

	invalid := []string{"", "database_url", "1KEY", "KEY-NAME", "KEY.NAME"}
	for _, value := range invalid {
		if secretKeyPattern.MatchString(value) {
			t.Fatalf("secretKeyPattern accepted %q", value)
		}
	}
}

func TestPageDataUsesGenericListShape(t *testing.T) {
	items := []postgres.Entity{{ID: "org-1"}}

	got := pageData(items, 7, postgres.Pagination{PageNum: 2, PageSize: 5})

	if got.PageNum != 2 {
		t.Fatalf("pageNum = %d, want 2", got.PageNum)
	}
	if got.PageSize != 5 {
		t.Fatalf("pageSize = %d, want 5", got.PageSize)
	}
	if got.Total != 7 {
		t.Fatalf("total = %d, want 7", got.Total)
	}
	list, ok := got.List.([]postgres.Entity)
	if !ok {
		t.Fatalf("list type = %T, want []postgres.Entity", got.List)
	}
	if len(list) != 1 || list[0].ID != "org-1" {
		t.Fatalf("list = %#v, want org-1", list)
	}
}
