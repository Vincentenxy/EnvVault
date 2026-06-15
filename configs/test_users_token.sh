#!/usr/bin/env bash
# =====================================================================
# 一键取 7 个测试用户的 dev token
# =====================================================================
#
# 前提:
#   1. envVault 服务端在跑(默认 http://localhost:8880)
#   2. configs/config.yaml 里 auth.dev_token_enabled = true
#   3. 已跑过 configs/test_users.sql 创建了 7 个用户
#
# 用法:
#   chmod +x configs/test_users_token.sh
#   ./configs/test_users_token.sh
#
# 输出:
#   - 在终端打印每个用户的 token 前 32 字符
#   - 导出 7 个环境变量:TOKEN_ALICE / TOKEN_BOB / ... / TOKEN_GRACE
#   - 当前 shell 里 source 后可以直接拿,例如:
#       eval "$(./configs/test_users_token.sh --export)"
#
# 兼容性:macOS / Linux 默认 bash,Git Bash。
# 依赖:curl + jq(没有 jq 也能跑,会退化为 python 解析)。
# =====================================================================

set -euo pipefail

BASE_URL="${ENVVAULT_BASE_URL:-http://localhost:8880}"

# 7 个 user,id 与名称与服务端 dev_user_id / dev_user_name 对齐
USERS=(
    "alice|Alice (project_admin)"
    "bob|Bob (project_developer)"
    "carol|Carol (project_viewer)"
    "dave|Dave (folder_editor/dev)"
    "eve|Eve (folder_viewer/dev)"
    "frank|Frank (org_viewer)"
    "grace|Grace (no binding)"
)

# JSON 字段抽取:有 jq 用 jq,没有用 python 兜底
extract_token() {
    local body="$1"
    if command -v jq >/dev/null 2>&1; then
        echo "$body" | jq -r '.data.token // empty'
    else
        echo "$body" | python -c "import json,sys; d=json.load(sys.stdin); print((d.get('data') or {}).get('token') or '')"
    fi
}

print_export_mode() {
    for entry in "${USERS[@]}"; do
        local id="${entry%%|*}"
        local name="${entry##*|}"
        local body
        body=$(curl -sX POST "${BASE_URL}/api/v1/auth/dev/token" \
            -H 'Content-Type: application/json' \
            -d "$(printf '{"userId":"%s","name":"%s","expiresInSeconds":3600}' "$id" "$name")")
        local token
        token=$(extract_token "$body")
        if [ -z "$token" ]; then
            echo "# WARN: failed to get token for $id (response: $body)" >&2
            continue
        fi
        local upper
        upper=$(echo "$id" | tr '[:lower:]' '[:upper:]')
        echo "export TOKEN_${upper}=\"${token}\""
    done
}

print_interactive_mode() {
    echo "==> getting dev tokens from ${BASE_URL}"
    for entry in "${USERS[@]}"; do
        local id="${entry%%|*}"
        local name="${entry##*|}"
        local body
        body=$(curl -sX POST "${BASE_URL}/api/v1/auth/dev/token" \
            -H 'Content-Type: application/json' \
            -d "$(printf '{"userId":"%s","name":"%s","expiresInSeconds":3600}' "$id" "$name")")
        local token
        token=$(extract_token "$body")
        local upper
        upper=$(echo "$id" | tr '[:lower:]' '[:upper:]')
        if [ -z "$token" ]; then
            printf "  %-7s %-30s  FAILED  body=%s\n" "$id" "$name" "$body"
        else
            printf "  %-7s %-30s  ok      token=%s...\n" "$id" "$name" "${token:0:32}"
            # 写到同目录 .tokens 文件,方便复制
            echo "export TOKEN_${upper}=\"${token}\"" >> "$(dirname "$0")/.tokens.env"
        fi
    done
    echo
    echo "==> 已写入 $(dirname "$0")/.tokens.env,在当前 shell 里 source 即可:"
    echo "    source configs/.tokens.env && echo \$TOKEN_ALICE"
}

case "${1:-}" in
    --export)
        print_export_mode
        ;;
    "")
        rm -f "$(dirname "$0")/.tokens.env"
        print_interactive_mode
        ;;
    -h|--help)
        sed -n '2,30p' "$0"
        ;;
    *)
        echo "usage: $0 [--export]" >&2
        exit 1
        ;;
esac
