package main

// 种子数据词表。
//
// 规模(中等,user 选定的):
//   20 orgs × 10 projects × 4 envs × 20 folders × 50 secrets
//   = 200 projects, 800 envs, 16000 folders, 800000 secret 记录
//
// 命名约定:
//   - org code  = "org-<index>"       (kebab-case,匹配 ^[a-z0-9]+(-[a-z0-9]+)*$)
//   - project code = "proj-<index>"
//   - folder code = "<service code>"    (20 个服务名,会跨 4 个 env 复用)
//   - secret key = 50 个固定 key 池     (^[A-Z][A-Z0-9_]*$)
//
// 所有 secret value 前缀 "[SEED-XXXX]" 标记为测试数据,便于后续清理。

// orgSpec 描述一个种子 org。
type orgSpec struct {
	Code    string
	Name    string
	Comment string
}

var orgSpecs = []orgSpec{
	{"org-01", "Alibaba", "alibaba seed org"},
	{"org-02", "Tencent", "tencent seed org"},
	{"org-03", "Bytedance", "bytedance seed org"},
	{"org-04", "Meituan", "meituan seed org"},
	{"org-05", "Pinduoduo", "pinduoduo seed org"},
	{"org-06", "JD", "jd seed org"},
	{"org-07", "Baidu", "baidu seed org"},
	{"org-08", "NetEase", "netease seed org"},
	{"org-09", "Xiaomi", "xiaomi seed org"},
	{"org-10", "Huawei", "huawei seed org"},
	{"org-11", "OPPO", "oppo seed org"},
	{"org-12", "Vivo", "vivo seed org"},
	{"org-13", "Didi", "didi seed org"},
	{"org-14", "Kuaishou", "kuaishou seed org"},
	{"org-15", "Bilibili", "bilibili seed org"},
	{"org-16", "Zhihu", "zhihu seed org"},
	{"org-17", "Xiaohongshu", "xiaohongshu seed org"},
	{"org-18", "Ctrip", "ctrip seed org"},
	{"org-19", "SFExpress", "sf express seed org"},
	{"org-20", "NIO", "nio seed org"},
}

// projectSpec 描述一个种子 project。
type projectSpec struct {
	Code    string
	Name    string
	Comment string
}

var projectSpecs = []projectSpec{
	{"proj-01", "UserCenter", "user center"},
	{"proj-02", "OrderSystem", "order system"},
	{"proj-03", "PaymentGateway", "payment gateway"},
	{"proj-04", "InventoryService", "inventory service"},
	{"proj-05", "SearchPlatform", "search platform"},
	{"proj-06", "RecommendEngine", "recommendation engine"},
	{"proj-07", "NotificationCenter", "notification center"},
	{"proj-08", "DataWarehouse", "data warehouse"},
	{"proj-09", "AdSystem", "ad system"},
	{"proj-10", "LiveStreaming", "live streaming"},
}

// envSpec 描述 4 个标准环境。code 与 /secrets/batchCreate 的 envList entry.envCode 字段对齐。
var envSpecs = []struct {
	Code    string
	Name    string
	Comment string
}{
	{"dev", "Development", "dev environment"},
	{"test", "Test", "test environment"},
	{"sim", "Simulation", "simulation environment"},
	{"prod", "Production", "production environment"},
}

// folderSpec 描述一个服务(folder)。20 个服务名会跨 4 env 复用。
type folderSpec struct {
	Code    string
	Name    string
	Comment string
}

var folderSpecs = []folderSpec{
	{"auth-svc", "AuthService", "auth service"},
	{"user-svc", "UserService", "user service"},
	{"profile-svc", "ProfileService", "profile service"},
	{"order-svc", "OrderService", "order service"},
	{"pay-svc", "PaymentService", "payment service"},
	{"inv-svc", "InventoryService", "inventory service"},
	{"search-svc", "SearchService", "search service"},
	{"rec-svc", "RecommendService", "recommend service"},
	{"notif-svc", "NotifyService", "notification service"},
	{"mq-svc", "MessageQueue", "message queue"},
	{"cache-svc", "CacheService", "cache service"},
	{"log-svc", "LogService", "log service"},
	{"mon-svc", "MonitorService", "monitor service"},
	{"cfg-svc", "ConfigService", "config service"},
	{"gw-svc", "GatewayService", "gateway service"},
	{"file-svc", "FileService", "file service"},
	{"img-svc", "ImageService", "image service"},
	{"vid-svc", "VideoService", "video service"},
	{"ana-svc", "AnalyticsService", "analytics service"},
	{"pipe-svc", "DataPipeline", "data pipeline"},
}

// secretKeySpec 描述一个 secret 字段。Comment 决定 value 的生成器。
type secretKeySpec struct {
	Key     string
	Comment string
	// kind 决定 value 生成器
	Kind string
}

var secretKeySpecs = []secretKeySpec{
	// Database (10)
	{"DB_HOST", "database host", "host"},
	{"DB_PORT", "database port", "port"},
	{"DB_NAME", "database name", "name"},
	{"DB_USER", "database user", "user"},
	{"DB_PASSWORD", "database password", "password"},
	{"DB_URL", "database url", "dburl"},
	{"DB_SSL_MODE", "ssl mode", "ssl"},
	{"DB_POOL_SIZE", "pool size", "int"},
	{"DB_MAX_CONN", "max conn", "int"},
	{"DB_TIMEOUT", "db timeout", "duration"},

	// Redis (5)
	{"REDIS_HOST", "redis host", "host"},
	{"REDIS_PORT", "redis port", "port"},
	{"REDIS_PASSWORD", "redis password", "password"},
	{"REDIS_DB", "redis db index", "int"},
	{"REDIS_CLUSTER_NODES", "redis cluster nodes", "csv"},

	// Message Queue (5)
	{"KAFKA_BROKERS", "kafka brokers", "csv"},
	{"KAFKA_TOPIC", "kafka topic", "name"},
	{"KAFKA_GROUP_ID", "kafka consumer group", "name"},
	{"RABBITMQ_URL", "rabbitmq url", "amqp"},
	{"RABBITMQ_VHOST", "rabbitmq vhost", "name"},

	// External API (10)
	{"STRIPE_API_KEY", "stripe api key", "stripe"},
	{"STRIPE_WEBHOOK_SECRET", "stripe webhook secret", "secret32"},
	{"ALIYUN_ACCESS_KEY", "aliyun access key", "ak"},
	{"ALIYUN_SECRET_KEY", "aliyun secret key", "secret32"},
	{"TENCENT_SECRET_ID", "tencent secret id", "ak"},
	{"TENCENT_SECRET_KEY", "tencent secret key", "secret32"},
	{"GOOGLE_MAPS_API_KEY", "google maps api key", "apikey"},
	{"SMS_API_KEY", "sms provider api key", "apikey"},
	{"EMAIL_API_TOKEN", "email provider token", "secret32"},
	{"PUSH_NOTIFICATION_KEY", "push notification key", "apikey"},

	// JWT / Token (6)
	{"JWT_SECRET", "jwt signing secret", "secret32"},
	{"JWT_EXPIRES_IN", "jwt expires in seconds", "int"},
	{"JWT_REFRESH_SECRET", "jwt refresh secret", "secret32"},
	{"SESSION_SECRET", "session secret", "secret32"},
	{"OAUTH_CLIENT_ID", "oauth client id", "name"},
	{"OAUTH_CLIENT_SECRET", "oauth client secret", "secret32"},

	// Server / SSH (6)
	{"SERVER_HOST", "server host", "host"},
	{"SERVER_PORT", "server port", "port"},
	{"SSH_HOST", "ssh host", "host"},
	{"SSH_PORT", "ssh port", "port"},
	{"SSH_USER", "ssh user", "user"},
	{"SSH_PRIVATE_KEY", "ssh private key", "sshkey"},

	// Observability (5)
	{"GRAFANA_TOKEN", "grafana api token", "secret32"},
	{"PROMETHEUS_TOKEN", "prometheus token", "secret32"},
	{"SLACK_WEBHOOK_URL", "slack webhook url", "url"},
	{"SENTRY_DSN", "sentry dsn", "url"},
	{"LOG_LEVEL", "log level", "loglevel"},

	// Misc (3)
	{"ENV_NAME", "env name", "name"},
	{"CLUSTER_NAME", "cluster name", "name"},
	{"SERVICE_VERSION", "service version", "version"},
}
