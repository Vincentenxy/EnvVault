package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	"envVault/internal/service"
)

// secretBatchEnvValue 是单个 env 下的「目标 folder + 待写入 value」。
//
// 客户端按 env code 显式指定目标 folderId(无需 template folder / 跨 env
// 同名 folder 推断),服务端在每个 env 的指定 folder 下 INSERT 一条 secret。
type secretBatchEnvValue struct {
	FolderId string `json:"folderId"`
	Value    string `json:"value"`
}

// secretBatchCreateItemRequest 单条 secret 的批量创建规格:
//
//	{
//	  "key":     "DATABASE_URL",
//	  "comment": "数据库url",
//	  "dev":     { "folderId": "<uuid>", "value": "..." },
//	  "test":    { "folderId": "<uuid>", "value": "..." },
//	  "sim":     { "folderId": "<uuid>", "value": "..." },
//	  "prod":    { "folderId": "<uuid>", "value": "..." }
//	}
//
// 每个 env 字段都是可选(指针);为 nil 表示「不在该 env 创建」。至少需要
// 指定一个 env,否则整条 item 无效。env 字段是硬编码的标准 4 个;非项目下
// 4 个标准 env 的项目暂不支持(后续可按需扩展)。
type secretBatchCreateItemRequest struct {
	Key     string               `json:"key"`
	Comment string               `json:"comment,omitempty"`
	Dev     *secretBatchEnvValue `json:"dev,omitempty"`
	Test    *secretBatchEnvValue `json:"test,omitempty"`
	Sim     *secretBatchEnvValue `json:"sim,omitempty"`
	Prod    *secretBatchEnvValue `json:"prod,omitempty"`
}

// secretBatchCreateRequest 接收 v11 batchCreate 端点入参。
//
// 路径:/api/v1/secrets/batchCreate(注意:复数 secrets,与单条 /secret/* 区分)。
type secretBatchCreateRequest struct {
	SecretList []secretBatchCreateItemRequest `json:"secretList"`
}

// envFieldBindings 把硬编码的 env 字段映射到 (envCode, value pointer) 对。
// 顺序与 SQL 单事务 INSERT 顺序一致,便于 audit 列举。
var envFieldBindings = []struct {
	envCode  string
	pickFunc func(*secretBatchCreateItemRequest) *secretBatchEnvValue
}{
	{"dev", func(it *secretBatchCreateItemRequest) *secretBatchEnvValue { return it.Dev }},
	{"test", func(it *secretBatchCreateItemRequest) *secretBatchEnvValue { return it.Test }},
	{"sim", func(it *secretBatchCreateItemRequest) *secretBatchEnvValue { return it.Sim }},
	{"prod", func(it *secretBatchCreateItemRequest) *secretBatchEnvValue { return it.Prod }},
}

// BatchCreateSecret 入口。所有响应 HTTP 200,业务码走 body.code:
//   - 成功 → 0 + "success"
//   - 任何失败 → -1 + "创建失败，" + 描述(参数错 / 权限 / 冲突 / 内部错误 一并)
func (ctrl *Controller) BatchCreateSecret(c *gin.Context) {
	var req secretBatchCreateRequest
	if !ctrl.bind(c, &req) {
		return
	}

	domainReq, err := secretBatchCreateRequestToDomain(req)
	if err != nil {
		// 入参校验失败(空 secretList、缺 key/folderId/value、key 格式错 等)
		// 走 HTTP 200 + body code=-1 + msg 描述,与业务错同一出口。
		writeBatchCreateError(c, "入参校验", err)
		return
	}

	ctrl.log(c, "BatchCreateSecret", logging.F("items", len(domainReq.SecretList)))
	user := auth.UserFromContext(c)
	if err := ctrl.secret.BatchCreate(c.Request.Context(), user, domainReq, ctrl.actor(c)); err != nil {
		writeBatchCreateError(c, "创建失败", err)
		return
	}
	response.OKWithCode(c, response.CodeSuccess, "success", nil)
}

// writeBatchCreateError 统一失败出口:HTTP 200 + body code=-1 + msg 拼接。
// 优先用 err 自身的 Error() 描述;domain sentinel 翻译成中文文案。
func writeBatchCreateError(c *gin.Context, prefix string, err error) {
	logging.Warn(c.Request.Context(), "writeBatchCreate", "request failed", logging.F("error", err))
	msg := prefix + "，" + err.Error()
	switch {
	case errors.Is(err, auth.ErrPermissionDenied):
		msg = prefix + "，权限不足"
	case errors.Is(err, domain.ErrNotFound):
		msg = prefix + "，目标 folder 不存在"
	case errors.Is(err, domain.ErrConflict):
		msg = prefix + "，secret 已存在"
	}
	response.OKWithCode(c, response.CodeBatchCreateError, msg, nil)
}

// secretBatchCreateRequestToDomain 把 HTTP DTO 翻译成 service.BatchCreateRequest,
// 同时做入参校验。校验失败返 error(controller 翻译为 code=-1)。
func secretBatchCreateRequestToDomain(req secretBatchCreateRequest) (service.BatchCreateRequest, error) {
	if len(req.SecretList) == 0 {
		return service.BatchCreateRequest{}, errors.New("secretList 不能为空")
	}
	out := make([]service.BatchCreateSecretSpec, 0, len(req.SecretList))
	for i, item := range req.SecretList {
		if !secretKeyPattern.MatchString(item.Key) {
			return service.BatchCreateRequest{}, fmt.Errorf("secretList[%d].key 格式错误,必须匹配 ^[A-Z][A-Z0-9_]*$", i)
		}
		envs, err := extractEnvs(i, item)
		if err != nil {
			return service.BatchCreateRequest{}, err
		}
		out = append(out, service.BatchCreateSecretSpec{
			Key:     item.Key,
			Comment: item.Comment,
			Envs:    envs,
		})
	}
	return service.BatchCreateRequest{SecretList: out}, nil
}

// extractEnvs 把单个 item 的 4 个 env 字段展开成有序的 []BatchCreateEnvTarget。
// 至少要有一个 env 非空,否则该 item 无效。
//
// 额外校验:同 item 内 4 个 env 字段的 folderId 必须互不相同。
// 若 dev/test/sim/prod 共用同一个 folderId(或部分共用),整批会因
// (folder_id, key) 唯一约束整批回滚,错误信息"secret 已存在"具有误导性。
// 在 controller 层提前 4xx 拒绝,定位更准。
func extractEnvs(itemIdx int, item secretBatchCreateItemRequest) ([]service.BatchCreateEnvTarget, error) {
	var envs []service.BatchCreateEnvTarget
	for _, b := range envFieldBindings {
		v := b.pickFunc(&item)
		if v == nil {
			continue
		}
		if strings.TrimSpace(v.FolderId) == "" {
			return nil, fmt.Errorf("secretList[%d].%s.folderId 不能为空", itemIdx, b.envCode)
		}
		envs = append(envs, service.BatchCreateEnvTarget{
			EnvCode:  b.envCode,
			FolderId: v.FolderId,
			Value:    v.Value,
		})
	}
	if len(envs) == 0 {
		return nil, fmt.Errorf("secretList[%d] 至少需要指定一个 env(dev/test/sim/prod)", itemIdx)
	}
	if err := checkEnvsFolderUniqueness(itemIdx, envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// checkEnvsFolderUniqueness 校验单 item 内各 env 字段的 folderId 互不相同。
// 重复时返回带 item index + 重复 env 列表 + 重复 folderId 的精确错误信息。
func checkEnvsFolderUniqueness(itemIdx int, envs []service.BatchCreateEnvTarget) error {
	folderIdToEnvs := make(map[string][]string, len(envs))
	for _, e := range envs {
		folderIdToEnvs[e.FolderId] = append(folderIdToEnvs[e.FolderId], e.EnvCode)
	}
	for folderId, envCodes := range folderIdToEnvs {
		if len(envCodes) > 1 {
			return fmt.Errorf(
				"secretList[%d] 的 env %v 共用了同一个 folderId(%s),应分别指向 4 个 env 下各自的 folder",
				itemIdx, envCodes, folderId,
			)
		}
	}
	return nil
}
