#!/usr/bin/env bash
# =====================================================================
# alice 调 search 返 1403 的诊断脚本
# =====================================================================
# 用法:
#   bash configs/diag_alice_403.sh \
#     'postgres://user:pass@host:5432/envvault?sslmode=disable' \
#     [http://localhost:8880]
#
# 5 步定位:用户存在 / binding 存在 / project UUID / JWT subject / 实际请求
# =====================================================================

set -euo pipefail

DBURL="${1:-}"
BASE_URL="${2:-http://localhost:8880}"

if [ -z "$DBURL" ]; then
    echo "usage: $0 <DATABASE_URL> [BASE_URL]" >&2
    exit 1
fi

PSQL="psql $DBURL -tA"

echo "=== 1. users 表里有没有 alice ==="
psql "$DBURL" -c "SELECT id, external_user_id, name, is_disabled FROM users WHERE external_user_id = 'alice';"

echo ""
echo "=== 2. alice 的所有 binding ==="
psql "$DBURL" -c "
SELECT r.code AS role, urb.scope_type,
       CASE urb.scope_type
         WHEN 'project' THEN (SELECT code FROM projects WHERE id = urb.scope_id)
         WHEN 'folder' THEN (SELECT code FROM folders WHERE id = urb.scope_id)
         WHEN 'environment' THEN (SELECT code FROM environments WHERE id = urb.scope_id)
         WHEN 'organization' THEN (SELECT code FROM organizations WHERE id = urb.scope_id)
         ELSE ''
       END AS scope_code
FROM user_role_bindings urb
JOIN users u ON u.id = urb.user_id
JOIN roles r ON r.id = urb.role_id
WHERE u.external_user_id = 'alice' AND urb.is_deleted = false;
"

echo ""
echo "=== 3. proj-09 的 UUID ==="
PROJ_ID=$(eval "$PSQL -c \"SELECT id FROM projects WHERE code = 'proj-09' AND org_id = (SELECT id FROM organizations WHERE code = 'org-01');\"")
echo "proj-09 id = $PROJ_ID"
if [ -z "$PROJ_ID" ]; then
    echo "ERROR: proj-09 不存在,需要先跑 cmd/seed" >&2
    exit 1
fi

echo ""
echo "=== 4. 拿 alice 的 token 并解 JWT claims ==="
RESP=$(curl -sX POST "$BASE_URL/api/v1/auth/dev/token" \
    -H 'Content-Type: application/json' \
    -d '{"userId":"alice","name":"Alice","expiresInSeconds":3600}')
TOKEN=$(echo "$RESP" | python -c "import json,sys; print(json.load(sys.stdin)['data']['token'])" 2>/dev/null || echo "")
if [ -z "$TOKEN" ]; then
    echo "ERROR: 拿 token 失败,响应: $RESP" >&2
    exit 1
fi
echo "Token (前 60 字符): ${TOKEN:0:60}..."
echo "JWT payload:"
echo "$TOKEN" | cut -d. -f2 | python -c "
import base64, json, sys
s = sys.stdin.read().strip()
s += '=' * (4 - len(s) % 4)
print(json.dumps(json.loads(base64.urlsafe_b64decode(s)), indent=2, ensure_ascii=False))
"

echo ""
echo "=== 5. 用 alice token 调 /secrets/search,projectId=$PROJ_ID ==="
curl -sX POST "$BASE_URL/api/v1/secrets/search" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"pageNum\":1,\"pageSize\":5,\"projectId\":\"$PROJ_ID\"}" | python -m json.tool 2>&1 | head -40

echo ""
echo "=== 6. 对照:用同一个 JWT,但 search 改成 env 维度(envId 留空,看 narrowing 行为) ==="
ENV_ID=$(eval "$PSQL -c \"SELECT id FROM environments WHERE code = 'dev' AND project_id = '$PROJ_ID';\"")
echo "dev env id = $ENV_ID"
curl -sX POST "$BASE_URL/api/v1/secrets/search" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"pageNum\":1,\"pageSize\":5,\"environmentId\":\"$ENV_ID\"}" | python -m json.tool 2>&1 | head -20
