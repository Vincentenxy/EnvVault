// Package controller 的 RBAC 相关 HTTP 处理器已按职责拆分到以下子文件：
//
//   - rbac_helpers.go    公共助手（ensureRBAC、allowScope、pagination）与请求类型
//   - rbac_permission.go 权限相关：ListPermissions、GetMyPermissions
//   - rbac_role.go       角色相关：ListRoles、GetRole、CreateRole、UpdateRole、DeleteRole
//   - rbac_binding.go    角色绑定相关：ListRoleBindings、GrantRole、RevokeRole
//   - rbac_user.go       RBAC 用户相关：GetCurrentRBACUser、ListRBACUsers、ListUserGrants、GetUserEffectivePermissions
//
// 本文件保留为导航索引，新增处理器时请按上述主题加入对应子文件。
package controller
