package domain

import "time"

// Permission is the read shape for permissions table. Code is the
// canonical string used in authorizer checks (e.g. "env:read").
type Permission struct {
	Id           string `json:"id"`
	Code         string `json:"code"`
	ResourceType string `json:"resourceType"`
	Action       string `json:"action"`
	Description  string `json:"description"`
	IsSystem     bool   `json:"isSystem"`
}

// Role is the read shape for roles table. Permissions is a denormalised
// list of permission codes populated by the service layer when listing.
type Role struct {
	Id          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ScopeType   string   `json:"scopeType"`
	OrgId       string   `json:"orgId,omitempty"`
	ProjectId   string   `json:"projectId,omitempty"`
	IsSystem    bool     `json:"isSystem"`
	Permissions []string `json:"permissions,omitempty" gorm:"-"`
}

// User is the read shape for users table.
//
// PasswordHash / PasswordAlgo / TokensValidAfter 是 v9 自注册 / 强制登出机制
// 引入的字段;PasswordHash/PasswordAlgo 永不出现在 JSON 响应里(json:"-"),
// 仅供 auth_service 内部 VerifyPassword / UpdatePasswordHash 用。
// TokensValidAfter 暴露给 list/get 响应,供前端展示「上次登出时间」之类的
// 调试信息;不暴露具体 hash 之前都是无害的。
type User struct {
	Id               string     `json:"id"`
	ExternalUserId   string     `json:"externalUserId"`
	Name             string     `json:"name"`
	Email            string     `json:"email"`
	Source           string     `json:"source"`
	IsDisabled       bool       `json:"isDisabled"`
	LastSeenAt       *time.Time `json:"lastSeenAt,omitempty"`
	PasswordHash     string     `json:"-"`
	PasswordAlgo     string     `json:"-"`
	TokensValidAfter *time.Time `json:"tokensValidAfter,omitempty"`
}

// RoleBinding is the read shape for user_role_bindings joined with users.
type RoleBinding struct {
	Id        string     `json:"id"`
	User      User       `json:"user"`
	RoleId    string     `json:"roleId"`
	RoleCode  string     `json:"roleCode"`
	ScopeType string     `json:"scopeType"`
	ScopeId   string     `json:"scopeId,omitempty"`
	GrantedBy string     `json:"grantedBy"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// EffectivePermissions is the response shape for "show me what this user
// can actually do in this scope" - useful for debugging & UI affordances.
type EffectivePermissions struct {
	Permissions  []string      `json:"permissions"`
	SourceGrants []RoleBinding `json:"sourceGrants"`
}

// RoleGrant 是 RoleBinding 的语义别名:SDK 视角下的"user_id / role_type / resource_id"三段式。
// 内部仍走 user_role_bindings 表(scope_id/scope_type),此处仅为 API 友好。
// 字段语义:
//   - UserId:用户数据库 ID(对应 users.id)
//   - RoleType:角色码(对应 roles.code,例如 "org_admin")
//   - ResourceType:作用域类型(对应 user_role_bindings.scope_type,例如 "organization")
//   - ResourceId:作用域 ID(对应 user_role_bindings.scope_id,global 时为空)
type RoleGrant struct {
	UserId       string     `json:"userId"`
	RoleType     string     `json:"roleType"`
	ResourceType string     `json:"resourceType"`
	ResourceId   string     `json:"resourceId,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	GrantedBy    string     `json:"grantedBy"`
	GrantedAt    time.Time  `json:"grantedAt"`
}

// ToGrant 把 RoleBinding 转换为 RoleGrant,字段一一映射。
func (b RoleBinding) ToGrant() RoleGrant {
	return RoleGrant{
		UserId:       b.User.Id,
		RoleType:     b.RoleCode,
		ResourceType: b.ScopeType,
		ResourceId:   b.ScopeId,
		ExpiresAt:    b.ExpiresAt,
		GrantedBy:    b.GrantedBy,
		GrantedAt:    b.CreatedAt,
	}
}
