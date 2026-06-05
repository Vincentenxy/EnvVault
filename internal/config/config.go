package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP     HTTPConfig     `mapstructure:"http"`
	Auth     AuthConfig     `mapstructure:"auth"`
	Security SecurityConfig `mapstructure:"security"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
}

type HTTPConfig struct {
	Addr            string `mapstructure:"addr"`
	RequestIdHeader string `mapstructure:"request_id_header"`
}

type AuthConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	PublicKey string `mapstructure:"public_key"`
	// v9 生产用私钥(RSA / ECDSA / Ed25519 PEM)。若空,Register / Login 不可用。
	PrivateKey      string `mapstructure:"private_key"`
	DevTokenEnabled bool   `mapstructure:"dev_token_enabled"`
	DevPrivateKey   string `mapstructure:"dev_private_key"`
	DevUserId       string `mapstructure:"dev_user_id"`
	DevUserName     string `mapstructure:"dev_user_name"`
	// v9 自注册开关;默认 true。false 时 POST /auth/register 返 403。
	RegisterEnabled bool `mapstructure:"register_enabled"`
	// v9 密码最小长度;默认 12。
	PasswordMinLength int `mapstructure:"password_min_length"`
	// v9 登录 IP 频控:窗口内允许的最大失败次数;默认 5。
	LoginRateLimit int `mapstructure:"login_rate_limit"`
	// v9 登录频控窗口;默认 1min。
	LoginRateLimitWindow time.Duration `mapstructure:"login_rate_limit_window"`
	// v9 触发 lockout 后的封禁时长;默认 15min。
	LockoutDuration time.Duration `mapstructure:"lockout_duration"`
	// v9 进程内 tokens_valid_after 缓存的全量刷新周期;默认 1min。
	TokensCacheRefresh time.Duration `mapstructure:"tokens_cache_refresh"`
	// v9 JWT 有效期;默认 24h。
	TokenTTL time.Duration `mapstructure:"token_ttl"`
}

type SecurityConfig struct {
	EncryptionKey string `mapstructure:"encryption_key"`
}

type DatabaseConfig struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Name            string        `mapstructure:"name"`
	SSLMode         string        `mapstructure:"ssl_mode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnectTimeout  time.Duration `mapstructure:"connect_timeout"`
}

type RedisConfig struct {
	Enabled       bool          `mapstructure:"enabled"`
	Mode          string        `mapstructure:"mode"`
	Addrs         []string      `mapstructure:"addrs"`
	Password      string        `mapstructure:"password"`
	DB            int           `mapstructure:"db"`
	KeyPrefix     string        `mapstructure:"key_prefix"`
	WarmUpOnStart bool          `mapstructure:"warm_up_on_start"`
	PoolSize      int           `mapstructure:"pool_size"`
	MinIdleConns  int           `mapstructure:"min_idle_conns"`
	MaxRetries    int           `mapstructure:"max_retries"`
	DialTimeout   time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout   time.Duration `mapstructure:"read_timeout"`
	WriteTimeout  time.Duration `mapstructure:"write_timeout"`
}

func Load() (Config, error) {
	return LoadFromPath(os.Getenv("ENVVAULT_CONFIG_PATH"))
}

func LoadFromPath(path string) (Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix("ENVVAULT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	applyComplexEnv(v)

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("./configs")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if path != "" || !errors.As(err, &notFound) {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if c.HTTP.Addr == "" {
		return fmt.Errorf("http.addr is required")
	}
	if c.HTTP.RequestIdHeader == "" {
		return fmt.Errorf("http.request_id_header is required")
	}
	if c.Database.Host == "" {
		return fmt.Errorf("database.host is required")
	}
	if c.Database.Port <= 0 {
		return fmt.Errorf("database.port must be positive")
	}
	if c.Database.User == "" {
		return fmt.Errorf("database.user is required")
	}
	if c.Database.Name == "" {
		return fmt.Errorf("database.name is required")
	}
	if c.Database.ConnectTimeout <= 0 {
		return fmt.Errorf("database.connect_timeout must be positive")
	}
	if c.Redis.Enabled {
		if c.Redis.Mode != "single" && c.Redis.Mode != "cluster" {
			return fmt.Errorf("redis.mode must be single or cluster")
		}
		if len(c.Redis.Addrs) == 0 {
			return fmt.Errorf("redis.addrs is required when redis is enabled")
		}
		if c.Redis.KeyPrefix == "" {
			return fmt.Errorf("redis.key_prefix is required when redis is enabled")
		}
	}
	return nil
}

func (c DatabaseConfig) DSN() string {
	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   c.Name,
	}

	query := dsn.Query()
	query.Set("sslmode", c.SSLMode)
	query.Set("connect_timeout", strconv.Itoa(int(c.ConnectTimeout.Seconds())))
	dsn.RawQuery = query.Encode()

	return dsn.String()
}

func applyComplexEnv(v *viper.Viper) {
	if addrs := os.Getenv("ENVVAULT_REDIS_ADDRS"); addrs != "" {
		parts := strings.Split(addrs, ",")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				values = append(values, part)
			}
		}
		v.Set("redis.addrs", values)
	}
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("http.addr", ":8080")
	v.SetDefault("http.request_id_header", "x-request-id")
	v.SetDefault("auth.enabled", true)
	v.SetDefault("auth.dev_token_enabled", false)
	v.SetDefault("auth.dev_user_id", "dev-user")
	v.SetDefault("auth.dev_user_name", "Dev User")
	v.SetDefault("auth.register_enabled", true)
	v.SetDefault("auth.password_min_length", 12)
	v.SetDefault("auth.login_rate_limit", 5)
	v.SetDefault("auth.login_rate_limit_window", time.Minute)
	v.SetDefault("auth.lockout_duration", 15*time.Minute)
	v.SetDefault("auth.tokens_cache_refresh", time.Minute)
	v.SetDefault("auth.token_ttl", 24*time.Hour)
	v.SetDefault("database.host", "127.0.0.1")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "admin")
	v.SetDefault("database.password", "123456")
	v.SetDefault("database.name", "envvault")
	v.SetDefault("database.ssl_mode", "disable")
	v.SetDefault("database.max_open_conns", 25)
	v.SetDefault("database.max_idle_conns", 5)
	v.SetDefault("database.conn_max_lifetime", "30m")
	v.SetDefault("database.connect_timeout", "5s")
	v.SetDefault("redis.enabled", true)
	v.SetDefault("redis.mode", "single")
	v.SetDefault("redis.addrs", []string{"127.0.0.1:6379"})
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.key_prefix", "envvault")
	v.SetDefault("redis.warm_up_on_start", true)
	v.SetDefault("redis.pool_size", 10)
	v.SetDefault("redis.min_idle_conns", 2)
	v.SetDefault("redis.max_retries", 3)
	v.SetDefault("redis.dial_timeout", "5s")
	v.SetDefault("redis.read_timeout", "3s")
	v.SetDefault("redis.write_timeout", "3s")
}
