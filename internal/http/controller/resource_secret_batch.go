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

// secretBatchEnvValue 是单个 env 下的「目标 folder + 待写入 value」三元组。
//
// 客户端按 env code 显式指定目标 folderId(无需 template folder / 跨 env
// 同名 folder 推断),服务端在每个 entry 指定 env code 对应的 folder 下
// INSERT 一条 secret。envCode 不限于 dev/test/sim/prod,项目下任意 env code 都
// 可作为入参(由 service 层做"env 是否存在 / 是否有 secret:create 权限"判定)。
type secretBatchEnvValue struct {
	EnvCode  string `json:"envCode"`
	FolderId string `json:"folderId"`
	Value    string `json:"value"`
}

// secretBatchCreateItemRequest 单条 secret 的批量创建规格:
//
//	{
//	  "key":     "DATABASE_URL",
//	  "comment": "数据库url",
//	  "envList": [
//	    { "envCode": "dev",  "folderId": "<uuid>", "value": "..." },
//	    { "envCode": "test", "folderId": "<uuid>", "value": "..." },
//	    { "envCode": "sim",  "folderId": "<uuid>", "value": "..." },
//	    { "envCode": "prod", "folderId": "<uuid>", "value": "..." }
//	  ]
//	}
//
// envList 至少 1 项,每个 entry 都会在 service 层做独立的 secret:create 权限 check +
// 加密,单事务 N 条 INSERT + 1 条 batch audit。entry 数量 = 该 key 要创建的 secret 数。
type secretBatchCreateItemRequest struct {
	Key     string                `json:"key"`
	Comment string                `json:"comment,omitempty"`
	EnvList []secretBatchEnvValue `json:"envList"`
}

// secretBatchCreateRequest 接收 v11 batchCreate 端点入参。
//
// 路径:/api/v1/secrets/batchCreate(注意:复数 secrets,与单条 /secret/* 区分)。
type secretBatchCreateRequest struct {
	SecretList []secretBatchCreateItemRequest `json:"secretList"`
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
		// 入参校验失败(空 secretList、缺 key/envCode/folderId/value、key 格式错 等)
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

// extractEnvs 把单个 item 的 envList 字段展开成有序的 []BatchCreateEnvTarget。
// 校验项:
//   - envList 非空
//   - 每条 envCode / folderId 非空(允许 value 为空字符串,空 secret 业务上可能合法)
//   - 同 item 内 envCode 不可重复(防止「同 env 下建两条同 key 的 secret」语义错)
//   - 同 item 内 folderId 不可重复(防止 DB (folder_id, key) 唯一约束冲突,
//     错误信息比「secret 已存在」更精准,client 可立刻定位)
func extractEnvs(itemIdx int, item secretBatchCreateItemRequest) ([]service.BatchCreateEnvTarget, error) {
	if len(item.EnvList) == 0 {
		return nil, fmt.Errorf("secretList[%d].envList 至少需要指定一个 env", itemIdx)
	}
	envs := make([]service.BatchCreateEnvTarget, 0, len(item.EnvList))
	for ei, e := range item.EnvList {
		envCode := strings.TrimSpace(e.EnvCode)
		if envCode == "" {
			return nil, fmt.Errorf("secretList[%d].envList[%d].envCode 不能为空", itemIdx, ei)
		}
		if strings.TrimSpace(e.FolderId) == "" {
			return nil, fmt.Errorf("secretList[%d].envList[%d](envCode=%q).folderId 不能为空", itemIdx, ei, envCode)
		}
		envs = append(envs, service.BatchCreateEnvTarget{
			EnvCode:  envCode,
			FolderId: e.FolderId,
			Value:    e.Value,
		})
	}
	if err := checkEnvCodeUniqueness(itemIdx, envs); err != nil {
		return nil, err
	}
	if err := checkFolderIdUniqueness(itemIdx, envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// checkEnvCodeUniqueness 校验单 item 内 envCode 不重复。
// 重复时返回带 item index + 重复 envCode + 该 envCode 出现位置的精确错误信息。
func checkEnvCodeUniqueness(itemIdx int, envs []service.BatchCreateEnvTarget) error {
	seen := make(map[string][]int, len(envs))
	for i, e := range envs {
		seen[e.EnvCode] = append(seen[e.EnvCode], i)
	}
	for envCode, positions := range seen {
		if len(positions) > 1 {
			return fmt.Errorf(
				"secretList[%d] 的 envCode %q 重复出现于 envList[%v],每个 envCode 只能出现一次",
				itemIdx, envCode, positions,
			)
		}
	}
	return nil
}

// checkFolderIdUniqueness 校验单 item 内 folderId 不重复。
// 重复时返回带 item index + 复用 envCode 列表 + 复用 folderId 的精确错误信息。
// 若多个 envCode 指向同一 folderId,DB 端 (folder_id, key) 唯一约束会整批回滚,
// 错误信息 "secret 已存在" 具有误导性;controller 层提前拒绝,定位更准。
func checkFolderIdUniqueness(itemIdx int, envs []service.BatchCreateEnvTarget) error {
	folderIdToEnvs := make(map[string][]string, len(envs))
	for _, e := range envs {
		folderIdToEnvs[e.FolderId] = append(folderIdToEnvs[e.FolderId], e.EnvCode)
	}
	for folderId, envCodes := range folderIdToEnvs {
		if len(envCodes) > 1 {
			return fmt.Errorf(
				"secretList[%d] 的 env %v 共用了同一个 folderId(%s),应分别指向各自 env 下的 folder",
				itemIdx, envCodes, folderId,
			)
		}
	}
	return nil
}
