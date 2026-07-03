package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"envVault/internal/auth"
	"envVault/internal/domain"
	"envVault/internal/http/response"
	"envVault/internal/logging"
	rediscache "envVault/internal/store/redis"
)

// =============================================================================
// 批量更新 Folder 接口
//
// 本文件是独立于 resource_folder.go 的批量操作文件，新增的批量更新接口放在这里，
// 方便后续查看和维护。不要将本文件的内容合并到 resource_folder.go 中。
//
// 端点: POST /api/v1/folder/batchUpdate
//
// 请求体:
//
//	{
//	    "idList": ["uuid-1", "uuid-2"],
//	    "code": "",
//	    "name": "magic-v2-svc-test",
//	    "comment": "测试服务1"
//	}
//
// 逻辑:
//  1. idList 中的 folderId 对应的 code 必须全部相同，否则返回更新失败
//  2. 可更新字段: name, comment
//  3. 权限校验与 /api/v1/folder/update 接口一致（folder:update）
//  4. 更新成功后同步 Redis cache
// =============================================================================

// batchUpdateFolderRequest 是 POST /api/v1/folder/batchUpdate 的请求体。
//
// 字段说明:
//   - IdList: 要更新的 folder id 列表，至少 1 个，最多 100 个
//   - Code:   用于校验所有 folder 的 code 是否一致。如果 idList 中的 folder
//     对应的 code 与请求中的 code 不一致，则拒绝更新。
//     注意: code 本身不可修改，仅用于校验一致性。
//   - Name:   新的 folder 名称（可更新字段）
//   - Comment: 新的 folder 备注（可更新字段）
type batchUpdateFolderRequest struct {
	IdList  []string `json:"idList"`
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Comment string   `json:"comment"`
}

// BatchUpdateFolders 批量更新 folder 的 name 和 comment。
//
// 与 UpdateFolder（单条更新）共享相同的权限校验逻辑:
//   - 权限码: folder:update
//   - scopeType: folder
//   - scopeId: 每个 folder 的 id
//
// 额外校验:
//   - idList 非空且每个 id 都是合法 UUID
//   - idList 中所有 folder 的 code 必须与请求中的 code 一致
//   - code 不可修改，仅用于一致性校验
func (ctrl *Controller) BatchUpdateFolders(c *gin.Context) {
	var req batchUpdateFolderRequest
	if !ctrl.bind(c, &req) {
		return
	}

	// 校验 idList
	if len(req.IdList) == 0 {
		response.FailWithMsg(c, "idList is required and cannot be empty")
		return
	}
	if len(req.IdList) > 100 {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "idList max length is 100")
		return
	}
	for _, id := range req.IdList {
		if !uuidPattern.MatchString(id) {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidRequest, "idList contains invalid uuid: "+id)
			return
		}
	}

	// 去重
	cleanedIds := make([]string, 0, len(req.IdList))
	seen := make(map[string]struct{}, len(req.IdList))
	for _, id := range req.IdList {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleanedIds = append(cleanedIds, id)
	}
	if len(cleanedIds) == 0 {
		response.FailWithMsg(c, "idList is required and cannot be empty")
		return
	}

	ctrl.log(c, "BatchUpdateFolders", logging.F("id_list_len", len(cleanedIds)), logging.F("code", req.Code), logging.F("name", req.Name))

	ctx := c.Request.Context()
	actor := ctrl.actor(c)
	user := auth.UserFromContext(c)

	// 1. 查询所有 folder 的当前 code，校验一致性
	//    一次性查询所有 folder，避免 N+1
	type folderCodeInfo struct {
		Id   string
		Code string
	}
	folderInfos := make([]folderCodeInfo, 0, len(cleanedIds))
	for _, id := range cleanedIds {
		folder, err := ctrl.repo.GetFolder(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				response.Fail(c, http.StatusNotFound, response.CodeNotFound, "folder not found: "+id)
				return
			}
			ctrl.write(c, nil, err)
			return
		}
		folderInfos = append(folderInfos, folderCodeInfo{Id: folder.Id, Code: folder.Code})
	}

	// 2. 校验所有 folder 的 code 是否一致
	//    如果请求中传了 code，则所有 folder 的 code 必须等于请求中的 code
	//    如果请求中没传 code，则所有 folder 的 code 必须彼此相同
	reqCode := strings.TrimSpace(req.Code)
	for _, fi := range folderInfos {
		if reqCode != "" {
			if fi.Code != reqCode {
				response.FailWithMsg(c, "batch update failed: folder code mismatch, expected all folders to have code '"+reqCode+"' but folder '"+fi.Id+"' has code '"+fi.Code+"'")
				return
			}
		} else {
			// 如果请求没传 code，则用第一个 folder 的 code 作为基准
			if reqCode == "" {
				reqCode = fi.Code
			}
			if fi.Code != reqCode {
				response.FailWithMsg(c, "batch update failed: folder code mismatch, expected all folders to have code '"+reqCode+"' but folder '"+fi.Id+"' has code '"+fi.Code+"'")
				return
			}
		}
	}

	// 3. 权限校验:对每个 folder 校验 folder:update 权限
	for _, fi := range folderInfos {
		if err := ctrl.authorizer.Allow(ctx, user, "folder:update", auth.Resource{
			Type: "folder",
			Id:   fi.Id,
		}); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				response.Fail(c, http.StatusForbidden, response.CodeForbidden, "permission denied: folder:update on folder '"+fi.Id+"'")
				return
			}
			ctrl.write(c, nil, err)
			return
		}
	}

	// 4. 批量更新
	updated := make([]Entity, 0, len(cleanedIds))
	for _, id := range cleanedIds {
		item, err := ctrl.repo.UpdateFolder(ctx, id, req.Name, req.Comment, actor)
		if err != nil {
			ctrl.write(c, nil, err)
			return
		}
		updated = append(updated, item)
	}

	// 5. 同步 Redis cache
	for _, item := range updated {
		envId, projectId, parentFolderId, level, ctxErr := ctrl.repo.GetFolderContext(ctx, item.Id)
		if ctxErr != nil {
			continue
		}
		ctrl.cacheUpsert(c, func(rc *rediscache.Cache) error {
			return rc.UpsertFolder(ctx, item, envId, projectId, parentFolderId, level)
		})
	}

	ctrl.write(c, updated, nil)
}
