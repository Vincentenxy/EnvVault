package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient 是 seed 程序专用的 HTTP 客户端:
//   - 10s 超时
//   - 自动带 Authorization header
//   - 解析统一响应壳 {code, msg, data}
type httpClient struct {
	baseURL string
	token   string
	hc      *http.Client
}

func newClient(baseURL, token string) *httpClient {
	return &httpClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// envelope 镜像 envVault 内部 Response[T] 形状。
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// call 发起 POST 请求,自动带 JWT。
// 业务码 0 才视为成功;非 0 / 非 2xx 都视为错误,带回 msg。
//
// tolerateConflict:若为 true,biz code=-1 且 msg 含 "已存在"/"conflict" 时返回 nil
// (用于 seed 重入场景:org/project/folder/secret 已存在则静默跳过)。
func (c *httpClient) call(ctx context.Context, path string, body any, out any, tolerateConflict bool) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	// 4xx/5xx 也尝试解析 envelope,因为业务错可能 200+code=-1(batch create)。
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode envelope: %w (body=%s)", err, string(raw))
	}
	if env.Code != 0 {
		// 重入:org/project/folder/secret 已存在时静默跳过。
		// controller 把 ErrConflict 翻译为 "secret 已存在" / "code 已存在" 等中文文案。
		if tolerateConflict && env.Code == -1 && isConflictMsg(env.Msg) {
			return nil
		}
		return fmt.Errorf("biz code=%d msg=%s", env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decode data: %w (raw=%s)", err, string(env.Data))
		}
	}
	return nil
}

// isConflictMsg 判定中文错误文案是否表示"已存在"类的幂等冲突。
// 当前命中:
//   - "secret 已存在"
//   - "目标 folder 不存在"   (secretBatchCreateRequestToDomain 的 not-found,不会触发)
//   - "权限不足"             (ErrPermissionDenied)
//
// 我们只把"已存在" 视为可跳过,其他错仍 fail-fast。
func isConflictMsg(msg string) bool {
	return strings.Contains(msg, "已存在") || strings.Contains(strings.ToLower(msg), "conflict")
}

// getDevToken 调 /auth/dev/token 拿 JWT(本地测试专用,dev_token_enabled=true 时可用)。
// 返回 token 字符串。
func (c *httpClient) getDevToken(ctx context.Context) (string, error) {
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	// /auth/dev/token 可接受空 body,服务端会用配置的 dev_user_id。
	if err := c.call(ctx, "/api/v1/auth/dev/token", nil, &resp, false); err != nil {
		return "", err
	}
	if resp.Token == "" {
		return "", fmt.Errorf("empty token in dev token response")
	}
	return resp.Token, nil
}
