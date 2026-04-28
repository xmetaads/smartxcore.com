package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Agent    AgentConfig
	Email    EmailConfig
	Storage  StorageConfig
	Limits   LimitsConfig
	CORS     CORSConfig
}

type ServerConfig struct {
	Port           int
	Environment    string
	LogLevel       string
	TrustedProxies []string
}

type DatabaseConfig struct {
	URL            string
	MaxConnections int32
	MinConnections int32
	AutoMigrate    bool
	MigrationsDir  string
}

type AuthConfig struct {
	JWTSecret           string
	AccessTokenTTL      time.Duration
	RefreshTokenTTL     time.Duration
}

type AgentConfig struct {
	TokenLength             int
	OnboardingCodeTTLHours  int
}

type EmailConfig struct {
	Host         string
	Port         int
	Username     string
	Password     string
	From         string
	AlertEmail   string
	DashboardURL string
}

type StorageConfig struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

type LimitsConfig struct {
	AgentPerMinute int
	AdminPerMinute int
}

type CORSConfig struct {
	AllowedOrigins []string
}

func Load() (*Config, error) {
	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Defaults
	v.SetDefault("PORT", 8080)
	v.SetDefault("ENVIRONMENT", "development")
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("DB_MAX_CONNECTIONS", 20)
	v.SetDefault("DB_MIN_CONNECTIONS", 5)
	v.SetDefault("DB_AUTO_MIGRATE", true)
	v.SetDefault("DB_MIGRATIONS_DIR", "./migrations")
	v.SetDefault("JWT_ACCESS_TTL_MINUTES", 15)
	v.SetDefault("JWT_REFRESH_TTL_DAYS", 30)
	v.SetDefault("AGENT_TOKEN_LENGTH", 64)
	v.SetDefault("ONBOARDING_CODE_TTL_HOURS", 72)
	v.SetDefault("RATELIMIT_AGENT_PER_MINUTE", 120)
	v.SetDefault("RATELIMIT_ADMIN_PER_MINUTE", 60)

	// Try .env file (optional)
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	_ = v.ReadInConfig()

	cfg := &Config{
		Server: ServerConfig{
			Port:           v.GetInt("PORT"),
			Environment:    v.GetString("ENVIRONMENT"),
			LogLevel:       v.GetString("LOG_LEVEL"),
			TrustedProxies: splitCSV(v.GetString("TRUSTED_PROXIES")),
		},
		Database: DatabaseConfig{
			URL:            v.GetString("DATABASE_URL"),
			MaxConnections: int32(v.GetInt("DB_MAX_CONNECTIONS")),
			MinConnections: int32(v.GetInt("DB_MIN_CONNECTIONS")),
			AutoMigrate:    v.GetBool("DB_AUTO_MIGRATE"),
			MigrationsDir:  v.GetString("DB_MIGRATIONS_DIR"),
		},
		Auth: AuthConfig{
			JWTSecret:       v.GetString("JWT_SECRET"),
			AccessTokenTTL:  time.Duration(v.GetInt("JWT_ACCESS_TTL_MINUTES")) * time.Minute,
			RefreshTokenTTL: time.Duration(v.GetInt("JWT_REFRESH_TTL_DAYS")) * 24 * time.Hour,
		},
		Agent: AgentConfig{
			TokenLength:            v.GetInt("AGENT_TOKEN_LENGTH"),
			OnboardingCodeTTLHours: v.GetInt("ONBOARDING_CODE_TTL_HOURS"),
		},
		Email: EmailConfig{
			Host:         v.GetString("SMTP_HOST"),
			Port:         v.GetInt("SMTP_PORT"),
			Username:     v.GetString("SMTP_USERNAME"),
			Password:     v.GetString("SMTP_PASSWORD"),
			From:         v.GetString("SMTP_FROM"),
			AlertEmail:   v.GetString("ALERT_EMAIL"),
			DashboardURL: v.GetString("DASHBOARD_URL"),
		},
		Storage: StorageConfig{
			Endpoint:  v.GetString("S3_ENDPOINT"),
			Region:    v.GetString("S3_REGION"),
			Bucket:    v.GetString("S3_BUCKET"),
			AccessKey: v.GetString("S3_ACCESS_KEY"),
			SecretKey: v.GetString("S3_SECRET_KEY"),
		},
		Limits: LimitsConfig{
			AgentPerMinute: v.GetInt("RATELIMIT_AGENT_PER_MINUTE"),
			AdminPerMinute: v.GetInt("RATELIMIT_ADMIN_PER_MINUTE"),
		},
		CORS: CORSConfig{
			AllowedOrigins: splitCSV(v.GetString("CORS_ALLOWED_ORIGINS")),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.Auth.JWTSecret == "" || len(c.Auth.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if c.Server.Environment != "development" && c.Server.Environment != "staging" && c.Server.Environment != "production" {
		return fmt.Errorf("ENVIRONMENT must be development, staging, or production")
	}
	return nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
