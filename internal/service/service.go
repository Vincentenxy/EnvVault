// Package service 集中需要"业务编排"的能力:值加密、Redis 缓存、reveal 审计、
// 角色授权计算、bootstrap 等。透传型的 CRUD(Org / Project / Env / Folder / Audit)
// 直接走 store,handler → repo,不再造一层 service。
//
// 每个 service 都以接口 + 结构体的形式给出:
//   - 接口在 service 包的导出符号中,handler 只依赖接口
//   - 实现是不导出的小写结构体,通过 NewXxxService 构造函数注入到调用方
//   - 构造函数的形参是 store 包定义的接口,保证 service 完全可单测
package service
