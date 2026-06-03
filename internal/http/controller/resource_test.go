package controller

import (
	"testing"

	"envVault/internal/domain"
)

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
	items := []domain.Entity{{Id: "org-1"}}

	got := pageData(items, 7, domain.Pagination{PageNum: 2, PageSize: 5})

	if got.PageNum != 2 {
		t.Fatalf("pageNum = %d, want 2", got.PageNum)
	}
	if got.PageSize != 5 {
		t.Fatalf("pageSize = %d, want 5", got.PageSize)
	}
	if got.Total != 7 {
		t.Fatalf("total = %d, want 7", got.Total)
	}
	list, ok := got.List.([]domain.Entity)
	if !ok {
		t.Fatalf("list type = %T, want []domain.Entity", got.List)
	}
	if len(list) != 1 || list[0].Id != "org-1" {
		t.Fatalf("list = %#v, want org-1", list)
	}
}
