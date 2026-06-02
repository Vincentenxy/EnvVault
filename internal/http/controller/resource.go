// Package controller 的资源/凭据相关 HTTP 处理器已按职责拆分到以下子文件：
//
//   - resource_common.go        请求类型、分页、共享助手（bind/write/actor/log/encrypt/decrypt/cache）、校验器与正则
//   - resource_organization.go  组织：Create/List/Get/Update/Delete Organization
//   - resource_project.go       项目：Create/List/Get/Update/Delete Project
//   - resource_environment.go   环境：Create/List/Get/Update/Delete Environment
//   - resource_folder.go        文件夹：Create/List/Get/Update/Delete Folder
//   - resource_secret.go        凭据：Create/Update/Get/Reveal/List/Search/Delete Secret
//   - resource_audit.go         审计：ListAuditRecords
//
// 本文件保留为导航索引，新增处理器时请按上述主题加入对应子文件。
package controller
