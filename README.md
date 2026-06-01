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
- Paginated requests use `pageNum` and `pageSize`; paginated responses use `data.pageNum`, `data.pageSize`, `data.total`, and `data.list`.
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
