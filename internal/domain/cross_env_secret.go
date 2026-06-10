package domain

import (
	"encoding/json"
	"time"
)

// EnvSecretValue 是单个 env 下 secret 的 reveal 视图。
// 设计为指针使用,使得 map 中"未命中"可以表达为 nil → JSON null。
type EnvSecretValue struct {
	FolderId  string    `json:"folderId"`
	Value     string    `json:"value"`
	Version   int       `json:"version"`
	Comment   string    `json:"comment,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SecretAcrossEnvs 是"按 (folderCode, key) 跨若干 env reveal"的响应 DTO。
//
// 自定义 MarshalJSON 把 Envs 展平到顶层:
//
//	{ "projectCode":..., "key":..., "comment":...,
//	  "<envCode1>": {value, version, comment, updatedAt} | null, ... }
//
// Envs 中必须包含上送 envList 的所有 code(未命中的填 nil → JSON null),
// 这样前端可以按固定下标位访问,不会出现字段缺失。
//
// 不回显请求参数:projectId / folderCode 是请求入参,不再放进响应(避免冗余);
// projectCode 是从 secret 派生出来的标识,保留供前端展示。
type SecretAcrossEnvs struct {
	ProjectCode string                     `json:"projectCode"`
	Key         string                     `json:"key"`
	Comment     string                     `json:"comment,omitempty"`
	Envs        map[string]*EnvSecretValue `json:"-"` // 走自定义 MarshalJSON
}

func (s SecretAcrossEnvs) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 4+len(s.Envs))
	out["projectCode"] = s.ProjectCode
	out["key"] = s.Key
	if s.Comment != "" {
		out["comment"] = s.Comment
	}
	for envCode, val := range s.Envs {
		out[envCode] = val // nil 序列化为 null
	}
	return json.Marshal(out)
}
