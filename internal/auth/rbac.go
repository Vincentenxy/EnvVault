package auth

import (
	"context"
	"errors"
	"strings"
)

var ErrPermissionDenied = errors.New("permission denied")

type UserInfo struct {
	UserId string `json:"userId,omitempty"`
	Name   string `json:"name,omitempty"`
	JWT    string `json:"jwt,omitempty"`
	Cookie string `json:"cookie,omitempty"`
}

type Resource struct {
	Type string
	ID   string
}

type Scope struct {
	Type string
	ID   string
}

type Authorizer interface {
	Allow(ctx context.Context, user UserInfo, action string, resource Resource) error
}

type PermissionStore interface {
	ResourceScopes(ctx context.Context, resource Resource) ([]Scope, error)
	UserPermissions(ctx context.Context, externalUserID string, scopes []Scope) (map[string]struct{}, error)
}

type RBACAuthorizer struct {
	store PermissionStore
}

func NewRBACAuthorizer(store PermissionStore) *RBACAuthorizer {
	return &RBACAuthorizer{store: store}
}

func (a *RBACAuthorizer) Allow(ctx context.Context, user UserInfo, action string, resource Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return ErrPermissionDenied
	}
	if strings.TrimSpace(user.UserId) == "" {
		return ErrPermissionDenied
	}

	permission := permissionCode(action, resource.Type)
	if permission == "" {
		return ErrPermissionDenied
	}

	scopes, err := a.store.ResourceScopes(ctx, resource)
	if err != nil {
		return err
	}
	permissions, err := a.store.UserPermissions(ctx, user.UserId, scopes)
	if err != nil {
		return err
	}
	if _, ok := permissions[permission]; ok {
		return nil
	}
	return ErrPermissionDenied
}

type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Allow(context.Context, UserInfo, string, Resource) error {
	return nil
}

func permissionCode(action, resourceType string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		return ""
	}
	if strings.Contains(action, ":") {
		return action
	}
	resourceType = strings.TrimSpace(resourceType)
	if resourceType == "" {
		return ""
	}
	return permissionResourceType(resourceType) + ":" + action
}

func permissionResourceType(resourceType string) string {
	switch strings.TrimSpace(resourceType) {
	case "organization":
		return "org"
	case "environment":
		return "env"
	default:
		return strings.TrimSpace(resourceType)
	}
}
