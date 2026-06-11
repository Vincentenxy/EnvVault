// Package main 是 envVault 的种子数据写入工具。
//
// 用法:
//
//	go run ./cmd/seed -url=http://localhost:8880
//	go run ./cmd/seed -url=http://localhost:8880 -concurrency=16 -dry-run=false
//
// 工作流程(共 5 步):
//
//  1. POST /api/v1/auth/dev/token  拿 dev JWT(本地测试,需 dev_token_enabled=true)
//  2. 20 个 org:           POST /api/v1/org/create                  (20 次)
//  3. 200 个 project:      POST /api/v1/project/create (+ envs)     (200 次,每次带 4 env)
//  4. env id 反查:         POST /api/v1/env/list                    (200 次,拿 4 个 env id)
//  5. 4000 个 folder:      POST /api/v1/folder/create (+ envList)   (4000 次,每次带 4 envList)
//  6. env 下 folder id 反查: POST /api/v1/folder/listByProject      (4000 次,拿 4 env 下 folder id)
//  7. 800000 个 secret:    POST /api/v1/secrets/batchCreate         (8000 次,每次 100 item × 4 env)
//
// 错误:envVault 是 HTTP 200 + body.code,业务错 code!=0 视为失败,本程序直接 fail-fast
// 不重试。重跑前需先清库,或改造为按 code 冲突 (Conflict) 自动跳过。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
)

// seed 程序的全局开关。
var (
	baseURL      = flag.String("url", "http://localhost:8880", "envVault API base URL")
	concurrency  = flag.Int("c", 8, "secret batch create 的并发 worker 数")
	secretsPerFt = flag.Int("secrets-per-folder", 50, "每个 folder 下要写入的 secret 数(默认 50)")
	batchSize    = flag.Int("batch-size", 25, "每个 /secrets/batchCreate 调用里的 item 数(每个 item 跨 4 env)")
	dryRun       = flag.Bool("dry-run", false, "只跑第 1-2 步(org + project),不做 folder/secret")
	skipFolder   = flag.Bool("skip-folder", false, "跳过 folder 和 secret,只建 org+project+env")
)

// domainEntity 镜像 domain.Entity 关键字段,够用即可。
type domainEntity struct {
	Id       string `json:"id"`
	ParentId string `json:"parentId,omitempty"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	Comment  string `json:"comment,omitempty"`
}

// pageResp 镜像 pageData 返回的 {list, total, pageNum, pageSize}。
type pageResp struct {
	List     []domainEntity `json:"list"`
	Total    int            `json:"total"`
	PageNum  int            `json:"pageNum"`
	PageSize int            `json:"pageSize"`
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("seed starting: url=%s concurrency=%d dryRun=%v skipFolder=%v",
		*baseURL, *concurrency, *dryRun, *skipFolder)

	// 第 1 步:拿 dev token。
	bootClient := newClient(*baseURL, "")
	token, err := bootClient.getDevToken(ctx)
	if err != nil {
		log.Fatalf("get dev token: %v", err)
	}
	log.Printf("got dev token (len=%d)", len(token))
	client := newClient(*baseURL, token)

	// 进度统计。
	var stats struct {
		orgs       atomic.Int64
		projects   atomic.Int64
		envLookups atomic.Int64
		folders    atomic.Int64
		secrets    atomic.Int64
		errors     atomic.Int64
	}
	defer func() {
		log.Printf("DONE: orgs=%d projects=%d folders=%d secrets=%d errors=%d",
			stats.orgs.Load(), stats.projects.Load(), stats.folders.Load(),
			stats.secrets.Load(), stats.errors.Load())
	}()

	// 第 2 步:20 个 org(幂等:已存在则 list 反查 id)。
	orgIDs := make([]string, 0, len(orgSpecs))
	orgCodeToID := make(map[string]string, len(orgSpecs))
	// 先 list 一遍,把已存在的 code → id 缓存住。
	var existingOrgs pageResp
	if err := client.call(ctx, "/api/v1/org/list", map[string]any{
		"pageNum":  1,
		"pageSize": 1000,
	}, &existingOrgs, false); err == nil {
		for _, o := range existingOrgs.List {
			orgCodeToID[o.Code] = o.Id
		}
		log.Printf("found %d pre-existing orgs in DB", len(orgCodeToID))
	}
	for i, spec := range orgSpecs {
		if existing, ok := orgCodeToID[spec.Code]; ok {
			orgIDs = append(orgIDs, existing)
			continue
		}
		var org domainEntity
		err := client.call(ctx, "/api/v1/org/create", map[string]any{
			"code":    spec.Code,
			"name":    spec.Name,
			"comment": spec.Comment,
		}, &org, true /* tolerateConflict: 并发场景兜底 */)
		if err != nil {
			log.Fatalf("org create %s: %v", spec.Code, err)
		}
		stats.orgs.Add(1)
		orgIDs = append(orgIDs, org.Id)
		orgCodeToID[spec.Code] = org.Id
		if (i+1)%5 == 0 {
			log.Printf("orgs progress: %d/%d", i+1, len(orgSpecs))
		}
	}
	log.Printf("orgs done: %d", stats.orgs.Load())

	if *dryRun {
		log.Printf("dry-run: stop here")
		return
	}

	// 第 3+4 步:每个 org 下 10 个 project,每次带 4 个 env。
	// 我们要保留 (projectId, envCode) -> envId 的映射,供后续 folder 步骤用。
	type projectCtx struct {
		projectID string
		envIDs    map[string]string // envCode -> envId
	}
	allProjects := make([]projectCtx, 0, len(orgIDs)*len(projectSpecs))
	for oi, orgID := range orgIDs {
		// 先 list 该 org 下已存在的 project code → id。
		projCodeToID := make(map[string]string, len(projectSpecs))
		var existingProjects pageResp
		_ = client.call(ctx, "/api/v1/project/list", map[string]any{
			"orgId":    orgID,
			"pageNum":  1,
			"pageSize": 1000,
		}, &existingProjects, false)
		for _, p := range existingProjects.List {
			projCodeToID[p.Code] = p.Id
		}
		for _, pspec := range projectSpecs {
			var projID string
			if existing, ok := projCodeToID[pspec.Code]; ok {
				projID = existing
			} else {
				envsReq := make([]map[string]any, 0, len(envSpecs))
				for _, e := range envSpecs {
					envsReq = append(envsReq, map[string]any{
						"code":    e.Code,
						"name":    e.Name,
						"comment": e.Comment,
					})
				}
				var proj domainEntity
				err := client.call(ctx, "/api/v1/project/create", map[string]any{
					"parentId":     orgID,
					"code":         pspec.Code,
					"name":         pspec.Name,
					"comment":      pspec.Comment,
					"environments": envsReq,
				}, &proj, true /* tolerateConflict: 并发场景兜底 */)
				if err != nil {
					log.Fatalf("project create %s/%s: %v", orgIDs[oi], pspec.Code, err)
				}
				stats.projects.Add(1)
				projID = proj.Id
			}
			// 反查 4 个 env 的 id。
			var envList pageResp
			if err := client.call(ctx, "/api/v1/env/list", map[string]any{
				"projectId": projID,
				"pageNum":   1,
				"pageSize":  100,
			}, &envList, false); err != nil {
				log.Fatalf("env list for project %s: %v", projID, err)
			}
			stats.envLookups.Add(1)
			envIDs := make(map[string]string, len(envList.List))
			for _, e := range envList.List {
				envIDs[e.Code] = e.Id
			}
			if len(envIDs) != 4 {
				log.Fatalf("project %s envs: got %d, want 4 (codes=%v)",
					projID, len(envIDs), envIDs)
			}
			allProjects = append(allProjects, projectCtx{projectID: projID, envIDs: envIDs})
		}
	}
	log.Printf("projects+envs done: %d projects, %d env lookups",
		stats.projects.Load(), stats.envLookups.Load())

	if *skipFolder {
		log.Printf("skip-folder: stop here")
		return
	}

	totalFolders := len(allProjects) * len(folderSpecs)
	jobs := make(chan folderJob, totalFolders)
	for _, pc := range allProjects {
		for _, fspec := range folderSpecs {
			jobs <- folderJob{
				projectID: pc.projectID,
				envIDs:    pc.envIDs,
				fspec:     fspec,
			}
		}
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobs {
				if err := processFolderJob(ctx, client, job, &stats); err != nil {
					stats.errors.Add(1)
					log.Printf("[w%d] folder %s/%s failed: %v",
						workerID, job.projectID, job.fspec.Code, err)
				}
			}
		}(w)
	}
	wg.Wait()

	log.Printf("all workers finished")
}

// processFolderJob 处理单个 (project, folder):
//  1. /folder/create 带 4 envList 一次创建
//  2. 响应 data 直接是 [Entity, ...],顺序对应 envList,按 env code 排序确认
//  3. 翻成 (envCode -> folderId) 映射
//  4. 分批调用 /secrets/batchCreate
func processFolderJob(
	ctx context.Context,
	client *httpClient,
	job folderJob,
	stats *struct {
		orgs       atomic.Int64
		projects   atomic.Int64
		envLookups atomic.Int64
		folders    atomic.Int64
		secrets    atomic.Int64
		errors     atomic.Int64
	},
) error {
	// 1) folder create(幂等:已存在则 listByProject 反查 id)。
	envList := make([]string, 0, len(job.envIDs))
	for _, c := range []string{"dev", "test", "sim", "prod"} {
		envList = append(envList, job.envIDs[c])
	}

	// 先 list 该 project 下已存在的 folder,按 (level=1) code → folder entity 索引。
	existingFolders := map[string]domainEntity{}
	var existing pageResp
	if err := client.call(ctx, "/api/v1/folder/list", map[string]any{
		"projectId": job.projectID,
		"pageNum":   1,
		"pageSize":  1000,
	}, &existing, false); err == nil {
		for _, f := range existing.List {
			existingFolders[f.Code] = f
		}
	}
	// 注:folder/list 是按 envId/parentId 过滤的,无法一次性列一个 project 下所有 level=1 folder。
	// 我们改成按 4 个 env 各 list 一次,合并。
	if len(existingFolders) == 0 {
		for _, envID := range job.envIDs {
			var envListResp pageResp
			if err := client.call(ctx, "/api/v1/folder/list", map[string]any{
				"environmentId": envID,
				"pageNum":       1,
				"pageSize":      1000,
			}, &envListResp, false); err == nil {
				for _, f := range envListResp.List {
					existingFolders[f.Code] = f
				}
			}
		}
	}

	folderIDByEnvCode := map[string]string{}
	if existing, ok := existingFolders[job.fspec.Code]; ok {
		// folder 已在至少一个 env 下存在 — 但我们要确认 4 个 env 都存在。
		// 简化:已存在就全跳过(假设第一次跑完整了),用 4 个 env 各 list 一次拿 id。
		for envCode, envID := range job.envIDs {
			var envFolderList pageResp
			_ = client.call(ctx, "/api/v1/folder/list", map[string]any{
				"environmentId": envID,
				"keyword":       job.fspec.Code,
				"pageNum":       1,
				"pageSize":      10,
			}, &envFolderList, false)
			for _, f := range envFolderList.List {
				if f.Code == job.fspec.Code {
					folderIDByEnvCode[envCode] = f.Id
					break
				}
			}
		}
		_ = existing
		if len(folderIDByEnvCode) == 4 {
			stats.folders.Add(1)
		} else {
			log.Printf("WARN: folder %s exists in some envs but not all: %v", job.fspec.Code, folderIDByEnvCode)
		}
	} else {
		var fcResp []domainEntity
		if err := client.call(ctx, "/api/v1/folder/create", map[string]any{
			"level":   1,
			"code":    job.fspec.Code,
			"name":    job.fspec.Name,
			"comment": job.fspec.Comment,
			"envList": envList,
		}, &fcResp, true /* tolerateConflict */); err != nil {
			return fmt.Errorf("folder create: %w", err)
		}
		if len(fcResp) != 4 {
			return fmt.Errorf("folder create returned %d, want 4", len(fcResp))
		}
		stats.folders.Add(1)
		folderIDByEnvCode = map[string]string{
			"dev":  fcResp[0].Id,
			"test": fcResp[1].Id,
			"sim":  fcResp[2].Id,
			"prod": fcResp[3].Id,
		}
	}

	if len(folderIDByEnvCode) != 4 {
		return fmt.Errorf("folder %s: got %d env ids, want 4 (have=%v)",
			job.fspec.Code, len(folderIDByEnvCode), folderIDByEnvCode)
	}

	// 3) 分批 secret。
	ctxTag := job.fspec.Code
	secretList := make([]map[string]any, 0, *batchSize)
	flushed := 0
	flush := func() error {
		if len(secretList) == 0 {
			return nil
		}
		// secretList 里的每个 item 是 secretBatchCreateItemRequest:
		//   {key, comment, envList: [{envCode, folderId, value}, ...]}
		if err := client.call(ctx, "/api/v1/secrets/batchCreate", map[string]any{
			"secretList": secretList,
		}, nil, true /* tolerateConflict: (folder,key) 冲突静默跳过 */); err != nil {
			return fmt.Errorf("batch create: %w", err)
		}
		stats.secrets.Add(int64(len(secretList) * 4))
		flushed += len(secretList) * 4
		secretList = secretList[:0]
		return nil
	}

	for i, ks := range secretKeySpecs {
		if i >= *secretsPerFt {
			break
		}
		envList := make([]map[string]any, 0, 4)
		for _, envCode := range []string{"dev", "test", "sim", "prod"} {
			val := generateSecretValue(ks.Kind, ctxTag+"-"+envCode)
			envList = append(envList, map[string]any{
				"envCode":  envCode,
				"folderId": folderIDByEnvCode[envCode],
				"value":    val,
			})
		}
		secretList = append(secretList, map[string]any{
			"key":     ks.Key,
			"comment": ks.Comment,
			"envList": envList,
		})
		if len(secretList) >= *batchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return nil
}

// folderJob 描述一个 (project, folder) 写入任务,投到 worker 池。
type folderJob struct {
	projectID string
	envIDs    map[string]string
	fspec     folderSpec
}
