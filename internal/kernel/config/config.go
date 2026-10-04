// Package config loads configuration from environment variables and validates
// it at startup (fail fast). Any variable X may instead be supplied as a file
// via X_FILE, which is how Docker secrets are delivered (FR-TEC-12).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	TLS      bool
}

type Storage struct {
	Driver    string // fs | s3
	Dir       string
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string
}

type Config struct {
	Env                string // dev | test | staging | production
	Version            string
	InstanceCode       string
	HTTPAddr           string
	DatabaseURL        string
	DatabaseReplicaURL string
	AppSecret          string
	PublicBaseURL      string // Staff App base URL (Back Office paths) used in e-mail links
	MemberPortalURL    string // Member & Guest Portal base URL (activation, booking links)
	WebsiteURL         string // public website base URL (manage booking links)
	AllowedOrigins     []string
	CookieSecure       bool
	SessionTTL         time.Duration
	AuditStrict        bool // report mutating 2xx responses without an audit entry
	SMTP               SMTP
	Storage            Storage
	LogLevel           string
	WorkerConcurrency  int
}

func get(key, def string) string {
	if f := os.Getenv(key + "_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // G304: *_FILE points at an operator-mounted secret
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) bool {
	v := get(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getInt(key string, def int) int {
	v := get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Load reads and validates configuration. requireDB=false allows commands
// like `openapi` to run without a database.
func Load(requireDB bool) (*Config, error) {
	env := get("ONECLUB_ENV", "dev")
	c := &Config{
		Env:                env,
		Version:            get("ONECLUB_VERSION", "dev"),
		InstanceCode:       get("ONECLUB_INSTANCE", "local"),
		HTTPAddr:           get("HTTP_ADDR", ":8080"),
		DatabaseURL:        get("DATABASE_URL", ""),
		DatabaseReplicaURL: get("DATABASE_REPLICA_URL", ""),
		AppSecret:          get("APP_SECRET", ""),
		PublicBaseURL:      strings.TrimRight(get("PUBLIC_BASE_URL", "http://localhost:5173"), "/"),
		MemberPortalURL:    strings.TrimRight(get("MEMBER_PORTAL_URL", "http://localhost:5174"), "/"),
		WebsiteURL:         strings.TrimRight(get("WEBSITE_URL", "http://localhost:3000"), "/"),
		CookieSecure:       getBool("COOKIE_SECURE", env == "staging" || env == "production"),
		SessionTTL:         time.Duration(getInt("SESSION_TTL_HOURS", 12)) * time.Hour,
		AuditStrict:        getBool("AUDIT_STRICT", env == "dev" || env == "test"),
		LogLevel:           get("LOG_LEVEL", "info"),
		WorkerConcurrency:  getInt("WORKER_CONCURRENCY", 20),
		SMTP: SMTP{
			Host:     get("SMTP_HOST", ""),
			Port:     getInt("SMTP_PORT", 587),
			Username: get("SMTP_USERNAME", ""),
			Password: get("SMTP_PASSWORD", ""),
			From:     get("SMTP_FROM", "OneClub <no-reply@oneclub.local>"),
			TLS:      getBool("SMTP_TLS", false),
		},
		Storage: Storage{
			Driver:    get("STORAGE_DRIVER", "fs"),
			Dir:       get("STORAGE_DIR", "./var/storage"),
			Endpoint:  get("S3_ENDPOINT", ""),
			Bucket:    get("S3_BUCKET", ""),
			AccessKey: get("S3_ACCESS_KEY", ""),
			SecretKey: get("S3_SECRET_KEY", ""),
			UseSSL:    getBool("S3_USE_SSL", true),
			Region:    get("S3_REGION", ""),
		},
	}
	for _, o := range strings.Split(get("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:5174,http://localhost:3000"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}

	var problems []string
	switch c.Env {
	case "dev", "test", "staging", "production":
	default:
		problems = append(problems, "ONECLUB_ENV must be dev|test|staging|production")
	}
	if requireDB {
		if c.DatabaseURL == "" {
			problems = append(problems, "DATABASE_URL is required")
		}
		if len(c.AppSecret) < 32 {
			problems = append(problems, "APP_SECRET must be at least 32 characters")
		}
	}
	if c.Env == "production" {
		if !c.CookieSecure {
			problems = append(problems, "COOKIE_SECURE must be true in production")
		}
		if c.SMTP.Host == "" {
			problems = append(problems, "SMTP_HOST is required in production")
		}
	}
	if c.Storage.Driver != "fs" && c.Storage.Driver != "s3" {
		problems = append(problems, "STORAGE_DRIVER must be fs or s3")
	}
	if len(problems) > 0 {
		return nil, errors.New("config: " + strings.Join(problems, "; "))
	}
	return c, nil
}

// ReplicaURL returns the reporting replica URL, falling back to the primary.
func (c *Config) ReplicaURL() string {
	if c.DatabaseReplicaURL != "" {
		return c.DatabaseReplicaURL
	}
	return c.DatabaseURL
}

func (c *Config) String() string {
	return fmt.Sprintf("env=%s instance=%s addr=%s", c.Env, c.InstanceCode, c.HTTPAddr)
}
