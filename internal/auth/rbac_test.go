package auth

import (
	"context"
	"errors"
	"testing"
)

type fakePermissionStore struct {
	scopes     map[string][]Scope
	permission map[string]map[string][]string
}

func (s fakePermissionStore) ResourceScopes(_ context.Context, resource Resource) ([]Scope, error) {
	scopes, ok := s.scopes[resource.Type+":"+resource.ID]
	if !ok {
		return nil, errors.New("not found")
	}
	return scopes, nil
}

func (s fakePermissionStore) UserPermissions(_ context.Context, externalUserID string, scopes []Scope) (map[string]struct{}, error) {
	values := make(map[string]struct{})
	userScopes := s.permission[externalUserID]
	for _, scope := range scopes {
		for _, permission := range userScopes[scope.Type+":"+scope.ID] {
			values[permission] = struct{}{}
		}
	}
	return values, nil
}

func TestRBACAuthorizerAllowsPermissionOnResourceScope(t *testing.T) {
	authorizer := NewRBACAuthorizer(fakePermissionStore{
		scopes: map[string][]Scope{
			"project:project-1": {
				{Type: "global"},
				{Type: "organization", ID: "org-1"},
				{Type: "project", ID: "project-1"},
			},
		},
		permission: map[string]map[string][]string{
			"user-1": {
				"project:project-1": {"project:update"},
			},
		},
	})

	err := authorizer.Allow(context.Background(), UserInfo{UserId: "user-1"}, "update", Resource{
		Type: "project",
		ID:   "project-1",
	})
	if err != nil {
		t.Fatalf("Allow() error = %v, want nil", err)
	}
}

func TestRBACAuthorizerInheritsPermissionFromAncestorScope(t *testing.T) {
	authorizer := NewRBACAuthorizer(fakePermissionStore{
		scopes: map[string][]Scope{
			"secret:secret-1": {
				{Type: "global"},
				{Type: "organization", ID: "org-1"},
				{Type: "project", ID: "project-1"},
				{Type: "environment", ID: "env-1"},
				{Type: "folder", ID: "folder-1"},
			},
		},
		permission: map[string]map[string][]string{
			"user-1": {
				"organization:org-1": {"secret:update"},
			},
		},
	})

	err := authorizer.Allow(context.Background(), UserInfo{UserId: "user-1"}, "secret:update", Resource{
		Type: "secret",
		ID:   "secret-1",
	})
	if err != nil {
		t.Fatalf("Allow() error = %v, want nil", err)
	}
}

func TestRBACAuthorizerDeniesMissingPermission(t *testing.T) {
	authorizer := NewRBACAuthorizer(fakePermissionStore{
		scopes: map[string][]Scope{
			"project:project-1": {
				{Type: "global"},
				{Type: "organization", ID: "org-1"},
				{Type: "project", ID: "project-1"},
			},
		},
		permission: map[string]map[string][]string{
			"user-1": {
				"project:project-1": {"project:read"},
			},
		},
	})

	err := authorizer.Allow(context.Background(), UserInfo{UserId: "user-1"}, "update", Resource{
		Type: "project",
		ID:   "project-1",
	})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Allow() error = %v, want ErrPermissionDenied", err)
	}
}

func TestRBACAuthorizerDeniesEmptyUser(t *testing.T) {
	authorizer := NewRBACAuthorizer(fakePermissionStore{})

	err := authorizer.Allow(context.Background(), UserInfo{}, "read", Resource{
		Type: "project",
		ID:   "project-1",
	})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Allow() error = %v, want ErrPermissionDenied", err)
	}
}

func TestRBACAuthorizerMapsOrganizationResourceToOrgPermission(t *testing.T) {
	authorizer := NewRBACAuthorizer(fakePermissionStore{
		scopes: map[string][]Scope{
			"organization:org-1": {
				{Type: "global"},
				{Type: "organization", ID: "org-1"},
			},
		},
		permission: map[string]map[string][]string{
			"user-1": {
				"organization:org-1": {"org:update"},
			},
		},
	})

	err := authorizer.Allow(context.Background(), UserInfo{UserId: "user-1"}, "update", Resource{
		Type: "organization",
		ID:   "org-1",
	})
	if err != nil {
		t.Fatalf("Allow() error = %v, want nil", err)
	}
}
