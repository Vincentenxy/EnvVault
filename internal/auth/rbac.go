package auth

import "context"

type UserInfo struct {
	StaffUserID string `json:"staffUserId,omitempty"`
	GxjId       string `json:"gxjId,omitempty"`
	StaffNo     string `json:"staffNo,omitempty"`
	Name        string `json:"name,omitempty"`
	JWT         string `json:"jwt,omitempty"`
	Cookie      string `json:"cookie,omitempty"`
}

type Resource struct {
	Type string
	ID   string
}

type Authorizer interface {
	Allow(ctx context.Context, user UserInfo, action string, resource Resource) error
}

type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Allow(context.Context, UserInfo, string, Resource) error {
	return nil
}
