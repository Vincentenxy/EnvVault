# EnvVault

EnvVault is a lightweight, self-hostable secret management platform inspired by Infisical. It is designed for teams that need a simple private deployment for organizing, storing, searching, and auditing application secrets.

## Features

- Organization, project, environment, folder, and secret hierarchy.
- Readable business paths using `org_code.project_code.env_code.folder_code.KEY`.
- Built-in default environments: `dev`, `test`, `sim`, and `prod`.
- Custom environments, such as `poc`, are supported.
- One-level folder structure with default folders: `globals` and `groups-secrets`.
- Secret entries contain `.env` style `key`, encrypted `value`, and `comment`.
- JWT authentication for externally issued tokens.
- **Local email + password authentication** (v9): self-registration, login, logout, change password.
  Passwords hashed with argon2id; rate-limited login with Redis sliding window; forced logout via
  per-user `tokens_valid_after` timestamp.
- RBAC authorization extension points.
- PostgreSQL persistence.
- Redis-backed secret search cache.
- Server-side encryption with AES-256-GCM by default.
- Pluggable encryption interface for custom encryption implementations.
- Audit records for create, update, and delete operations.
- Logical deletion with deleted-record snapshots.
- Key-based secret search, with value search reserved for future design.
- HTTP API powered by Gin.

## Project Status

EnvVault is currently in early development. The core architecture, configuration loading, PostgreSQL connection, base schema, JWT parsing, encryption interface, and first API routes are in place.

The project is not yet recommended for production use without additional review around permissions, migrations, operational hardening, and key management.

## Quick Start

Start PostgreSQL and Redis:

```bash
docker compose up -d postgres redis
```

Initialize the database schema:

Connect to the default `postgres` database first, then create the EnvVault database:

```sql
create database envvault
    with owner admin
    encoding 'UTF8';
```

Then connect to `envvault` and initialize tables:

```bash
psql "postgres://admin:123456@127.0.0.1:5432/envvault?sslmode=disable" -f configs/schema.sql
```

Run EnvVault:

```bash
go run .
```

Health checks:

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/readyz
```

## Configuration

The default configuration file is:

```text
configs/config.yaml
```

You can override it with:

```bash
ENVVAULT_CONFIG_PATH=./configs/config.yaml go run .
```

Common environment variables:

- `ENVVAULT_CONFIG_PATH`
- `ENVVAULT_HTTP_ADDR`
- `ENVVAULT_HTTP_REQUEST_ID_HEADER`
- `ENVVAULT_AUTH_ENABLED`
- `ENVVAULT_AUTH_PUBLIC_KEY`
- `ENVVAULT_AUTH_DEV_TOKEN_ENABLED`
- `ENVVAULT_AUTH_DEV_PRIVATE_KEY`
- `ENVVAULT_AUTH_DEV_USER_ID`
- `ENVVAULT_AUTH_DEV_USER_NAME`
- `ENVVAULT_AUTH_PRIVATE_KEY` (v9, PKCS8 PEM, used to sign login JWTs)
- `ENVVAULT_AUTH_REGISTER_ENABLED` (v9, default `true`, set `false` to disable `/auth/register`)
- `ENVVAULT_AUTH_PASSWORD_MIN_LENGTH` (v9, default `12`)
- `ENVVAULT_AUTH_LOGIN_RATE_LIMIT` (v9, default `5` failures per window)
- `ENVVAULT_AUTH_LOGIN_RATE_LIMIT_WINDOW` (v9, default `60s`)
- `ENVVAULT_AUTH_LOCKOUT_DURATION` (v9, default `15m`)
- `ENVVAULT_AUTH_TOKENS_CACHE_REFRESH` (v9, default `1m`)
- `ENVVAULT_AUTH_TOKEN_TTL` (v9, default `24h`)
- `ENVVAULT_BOOTSTRAP_ADMIN_USER_ID` (grants `platform_admin` on startup)
- `ENVVAULT_BOOTSTRAP_ADMIN_NAME`
- `ENVVAULT_SECURITY_ENCRYPTION_KEY`
- `ENVVAULT_DATABASE_HOST`
- `ENVVAULT_DATABASE_PORT`
- `ENVVAULT_DATABASE_USER`
- `ENVVAULT_DATABASE_PASSWORD`
- `ENVVAULT_DATABASE_NAME`
- `ENVVAULT_DATABASE_SSL_MODE`
- `ENVVAULT_REDIS_ENABLED`
- `ENVVAULT_REDIS_MODE`
- `ENVVAULT_REDIS_ADDRS`
- `ENVVAULT_REDIS_PASSWORD`
- `ENVVAULT_REDIS_DB`

Do not commit production secrets, JWT secrets, or encryption keys.

## API Style

EnvVault uses action-style HTTP APIs:

- Requests without request data use `GET`.
- `GET` does not carry a request body or business query parameters by default.
- Requests with data, including pagination, filters, IDs, and search keywords, use `POST` with a JSON body.
- Special link-style flows may use `GET` with query parameters.
- API request and response fields use camelCase, such as `parentId`, `folderId`, `scopeType`, and `externalUserId`.
- Paginated requests use `pageNum` and `pageSize`; paginated responses use `data.total` and `data.list`, and **on non-empty data** also include `data.pageNum` and `data.pageSize` (omitted on empty data — see `design/DESIGN.md`「分页响应 - 空数据形态」).
- HTTP status codes describe transport-level status. The response body `code` is the business code: `0` means success, `-1` means generic failure, and special failures use codes greater than or equal to `1000`.

All business responses follow this format:

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

See [design/DESIGN.md](design/DESIGN.md) for the current detailed design. OpenAPI files are in [design/api](design/api), including core APIs and RBAC APIs.

The full HTTP endpoint catalog (path, request/response schema, RBAC) is in
[design/api/core.yaml](design/api/core.yaml) (OpenAPI 3.0.3) and
[design/api/rbac.yaml](design/api/rbac.yaml). Render with any standard tool,
for example:

```bash
# Redocly CLI
npx @redocly/cli preview-docs design/api/core.yaml
# or Swagger UI
docker run --rm -p 8081:8080 -e SWAGGER_JSON=/tmp/core.yaml \
  -v "$PWD/design/api:/tmp" swaggerapi/swagger-ui
```

Key endpoints to start with:

- `POST /api/v1/tree/get` — fetch the full org/project/env/folder tree (cached in Redis, narrowed by caller RBAC).
- `POST /api/v1/folder/listByProject` — list all folders under a project, grouped by `code`; each group carries `envList` and `subFolders` (level=2 children, also grouped by code). Use this when the frontend needs a project-wide folder picker.
- `POST /api/v1/folder/create` — batch-create a folder across multiple envs in one transaction. `envList` is required and contains env ids (UUIDs); `level=1` creates top-level folders, `level=2` requires `parentCode` and looks up the matching `level=1` parent in each env.
- `POST /api/v1/secret/path/batchReveal` — bulk-reveal every secret in a folder path.
- `POST /api/v1/search/global` — Redis-backed cross-resource keyword search.

Example: call `listByProject` after `tree/get` to render a project-scoped folder picker:

```bash
# 1) Mint a dev JWT
curl -s -X POST http://localhost:8080/api/v1/auth/dev/token

# 2) Fetch the project-scoped folder tree
curl -s -X POST http://localhost:8080/api/v1/folder/listByProject \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"projectId":"11111111-1111-1111-1111-111111111111"}'

# 3) Batch-create a sub-folder under <payment> in 3 envs in one transaction
curl -s -X POST http://localhost:8080/api/v1/folder/create \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "level": 2,
    "code": "stripe",
    "name": "Stripe",
    "parentCode": "payment",
    "envList": [
      "11111111-1111-1111-1111-111111111111",
      "22222222-2222-2222-2222-222222222222",
      "33333333-3333-3333-3333-333333333333"
    ]
  }'
```

For local development, the easiest way to try the API is the dev-token flow:

```bash
# 1) Mint a dev JWT (only when ENVVAULT_AUTH_DEV_TOKEN_ENABLED=true)
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/dev/token | jq -r .data.token)

# 2) Call a protected endpoint
curl -s -X POST http://localhost:8080/api/v1/tree/get \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}'
```

## Development

Run tests:

```bash
go test ./...
```

Format code:

```bash
go fmt ./...
```

## Security

EnvVault stores secret values encrypted at rest. The default implementation uses AES-256-GCM and expects a base64-encoded 32-byte encryption key.

Please do not report security issues through public GitHub issues. Use a private reporting channel when one is published for this project.

## License

EnvVault is open source software licensed under the [MIT License](LICENSE).
