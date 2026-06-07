package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// 工具:确定性 seed 用的伪随机。
// 我们用 crypto/rand 生成真随机即可,不需要 reproducibility。

// randString 生成长度 n 的 [a-z0-9] 随机串,用于不需要人类可读的部分。
func randString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		b, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		out[i] = charset[b.Int64()]
	}
	return string(out)
}

// randHex 生成长度 n 字节的 hex (2n 字符)。
func randHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// randBase64 生成长度 n 字节的 base64。
func randBase64(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return base64.StdEncoding.EncodeToString(buf)
}

// generateSecretValue 按 kind 生成一个"看起来像真的"测试 secret value。
//
// 所有 value 前缀 "[SEED]" 标识测试数据,便于后续清理。
//   - host:     10.61.<a>.<b>           (内网 IP 风格)
//   - port:     5432 / 6379 / 9200 等常见端口
//   - name:     "<tag>-<random>"
//   - user:     "app_<tag>_<random>"
//   - password: "[SEED]<24 位 random>"
//   - dburl:    "postgres://user:pass@host:5432/db"
//   - ssl:      "require" / "verify-full" 等
//   - int:      0-65535
//   - duration: "30s" / "5m" / "1h"
//   - csv:      "host1:port1,host2:port2"
//   - amqp:     "amqp://user:pass@host:5672/vhost"
//   - stripe:   "sk_test_<random>"
//   - ak:       "LTAI<16位>"  (阿里云风格)
//   - secret32: "[SEED]<32位 base64>"
//   - apikey:   "sk_<22位>"
//   - url:      "https://<host>/<path>"
//   - loglevel: "info" / "debug" / "warn"
//   - version:  "v1.0.<short>"
//   - sshkey:   假装的 RSA 私钥块
func generateSecretValue(kind, ctxTag string) string {
	prefix := "[SEED]"
	tag := strings.ReplaceAll(strings.ToLower(ctxTag), "_", "-")
	switch kind {
	case "host":
		return fmt.Sprintf("10.61.%d.%d", randInt(1, 254), randInt(2, 254))
	case "port":
		ports := []int{5432, 6379, 9200, 9300, 5672, 15672, 3306, 27017, 2181, 9092}
		return fmt.Sprintf("%d", ports[randInt(0, len(ports)-1)])
	case "name":
		return fmt.Sprintf("%s-%s", tag, randString(8))
	case "user":
		return fmt.Sprintf("app_%s_%s", tag, randString(6))
	case "password":
		return prefix + randString(24)
	case "dburl":
		return fmt.Sprintf("postgres://app_%s:%s@10.61.%d.%d:5432/%s_prod?sslmode=require",
			tag, prefix+randString(16), randInt(1, 254), randInt(2, 254), tag)
	case "ssl":
		opts := []string{"require", "verify-full", "verify-ca", "disable"}
		return opts[randInt(0, len(opts)-1)]
	case "int":
		return fmt.Sprintf("%d", randInt(8, 4096))
	case "duration":
		opts := []string{"5s", "10s", "30s", "1m", "5m"}
		return opts[randInt(0, len(opts)-1)]
	case "csv":
		parts := make([]string, 3)
		for i := range parts {
			parts[i] = fmt.Sprintf("10.61.%d.%d:%d", randInt(1, 254), randInt(2, 254), 9092)
		}
		return strings.Join(parts, ",")
	case "amqp":
		return fmt.Sprintf("amqp://%s:%s@10.61.%d.%d:5672/%s",
			"app_"+tag, prefix+randString(16), randInt(1, 254), randInt(2, 254), tag)
	case "stripe":
		return "sk_test_" + randString(24)
	case "ak":
		return "LTAI" + randString(16)
	case "secret32":
		return prefix + randBase64(24)
	case "apikey":
		return "sk_" + randString(22)
	case "url":
		return fmt.Sprintf("https://hooks.%s.example.com/api/v1/%s", tag, randString(12))
	case "loglevel":
		opts := []string{"info", "debug", "warn", "error"}
		return opts[randInt(0, len(opts)-1)]
	case "version":
		return fmt.Sprintf("v1.0.%s", randString(6))
	case "sshkey":
		return "-----BEGIN RSA PRIVATE KEY-----\n" +
			randBase64(64) + "\n" +
			randBase64(64) + "\n" +
			"-----END RSA PRIVATE KEY-----"
	default:
		return prefix + randString(16)
	}
}

func randInt(min, max int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	return int(n.Int64()) + min
}
