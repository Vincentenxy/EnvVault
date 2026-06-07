package controller

import (
	"encoding/json"
	"testing"
)

// TestValidateEnvIdsForCreate_AllValidUUIDs 锁住合法 UUID 全部通过校验。
func TestValidateEnvIdsForCreate_AllValidUUIDs(t *testing.T) {
	valid := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"abcdef01-2345-6789-abcd-ef0123456789",
	}
	if got := validateEnvIdsForCreate(valid); got != "" {
		t.Errorf("valid UUIDs: %q, want empty", got)
	}
}

// TestValidateEnvIdsForCreate_InvalidID 锁住非 UUID 格式的项返错误。
func TestValidateEnvIdsForCreate_InvalidID(t *testing.T) {
	cases := [][]string{
		{"not-a-uuid"},
		{"11111111-1111-1111-1111-11111111111"},   // 少 1 位
		{"11111111-1111-1111-1111-1111111111111"}, // 多 1 位
		{"11111111-1111-1111-1111-11111111111Z"},  // 含非法字符
		{""},                                      // 空串
		{"dev"},                                   // env code(旧格式)
		{" 11111111-1111-1111-1111-111111111111"}, // 前导空格
	}
	for _, ids := range cases {
		if got := validateEnvIdsForCreate(ids); got == "" {
			t.Errorf("envList=%v should be rejected", ids)
		}
	}
}

// TestCreateFolderRequest_Level1Batch_JSON 锁住 level=1 + envList 反序列化:
// 无 parentCode;envList 是 env id(UUID)列表。
func TestCreateFolderRequest_Level1Batch_JSON(t *testing.T) {
	raw := `{
  "level": 1,
  "code": "globals",
  "name": "Globals",
  "comment": "顶层 folder 批量创建",
  "envList": [
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
    "33333333-3333-3333-3333-333333333333"
  ]
}`
	var req createFolderRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.Code != "globals" {
		t.Errorf("Code = %q, want globals", req.Code)
	}
	if req.Level != 1 {
		t.Errorf("Level = %d, want 1", req.Level)
	}
	if req.ParentCode != "" {
		t.Errorf("ParentCode = %q, want empty (level=1 不需要 parentCode)", req.ParentCode)
	}
	if len(req.EnvList) != 3 {
		t.Fatalf("EnvList len = %d, want 3", len(req.EnvList))
	}
	if got := validateEnvIdsForCreate(req.EnvList); got != "" {
		t.Errorf("envList should validate, got: %q", got)
	}
}

// TestCreateFolderRequest_Level2Batch_JSON 锁住 level=2 + envList + parentCode 反序列化
// (典型场景:跨 env 在同名父 folder 下挂子 folder)。
func TestCreateFolderRequest_Level2Batch_JSON(t *testing.T) {
	raw := `{
  "level": 2,
  "code": "child-folder-aaaa",
  "name": "child-folder aaaa",
  "comment": "测试子folder aaa",
  "parentCode": "payment",
  "envList": [
    "11111111-1111-1111-1111-111111111111",
    "22222222-2222-2222-2222-222222222222",
    "33333333-3333-3333-3333-333333333333"
  ]
}`
	var req createFolderRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.Code != "child-folder-aaaa" {
		t.Errorf("Code = %q, want child-folder-aaaa", req.Code)
	}
	if req.Level != 2 {
		t.Errorf("Level = %d, want 2", req.Level)
	}
	if req.ParentCode != "payment" {
		t.Errorf("ParentCode = %q, want payment", req.ParentCode)
	}
	if len(req.EnvList) != 3 {
		t.Fatalf("EnvList len = %d, want 3", len(req.EnvList))
	}
	if got := validateEnvIdsForCreate(req.EnvList); got != "" {
		t.Errorf("envList should validate, got: %q", got)
	}
}

// TestCreateFolderRequest_ParentIdFieldGone 锁住旧版 `parentId` 字段已废弃:
// 旧请求体传 parentId 时,该字段被忽略(handler 走 level 校验时不再读它)。
func TestCreateFolderRequest_ParentIdFieldGone(t *testing.T) {
	// 故意写一个含 parentId 的 JSON,验证 unmarshal 后 ParentId 字段被丢弃
	// (当前 struct 无该字段,自动忽略;ParentCode 仍按缺省为空)。
	raw := `{
  "level": 2,
  "code": "x",
  "name": "X",
  "parentId": "11111111-1111-1111-1111-111111111111",
  "envList": ["22222222-2222-2222-2222-222222222222"]
}`
	var req createFolderRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if req.ParentCode != "" {
		t.Errorf("ParentCode = %q, want empty (未传 parentCode 时不读旧 parentId)", req.ParentCode)
	}
}

// TestCreateFolderRequest_EmptyEnvList 锁住"envList 必填非空"在 unmarshal 层是合法的
// (空数组 / null 都可 unmarshal 成功),实际是否返 -1 是 handler 决定。
func TestCreateFolderRequest_EmptyEnvList(t *testing.T) {
	// 空数组
	raw1 := `{"level":1,"code":"x","name":"X","envList":[]}`
	var req1 createFolderRequest
	if err := json.Unmarshal([]byte(raw1), &req1); err != nil {
		t.Fatalf("Unmarshal empty array: %v", err)
	}
	if len(req1.EnvList) != 0 {
		t.Errorf("envList should be empty, got %v", req1.EnvList)
	}

	// 完全不传 envList
	raw2 := `{"level":1,"code":"x","name":"X"}`
	var req2 createFolderRequest
	if err := json.Unmarshal([]byte(raw2), &req2); err != nil {
		t.Fatalf("Unmarshal missing envList: %v", err)
	}
	if req2.EnvList != nil {
		t.Errorf("envList should be nil, got %v", req2.EnvList)
	}

	// 显式 null
	raw3 := `{"level":1,"code":"x","name":"X","envList":null}`
	var req3 createFolderRequest
	if err := json.Unmarshal([]byte(raw3), &req3); err != nil {
		t.Fatalf("Unmarshal null: %v", err)
	}
	if req3.EnvList != nil {
		t.Errorf("envList should be nil, got %v", req3.EnvList)
	}

	// 提示:handler 在 len(req.EnvList) == 0 时返 -1;此测试仅锁住 unmarshal 行为。
}
