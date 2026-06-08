// Package config 负责加载和解析 YAML 配置文件。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是 RSSGen 的顶层配置结构。
type Config struct {
	Server    ServerConfig            `yaml:"server"`
	Storage   StorageConfig           `yaml:"storage"`
	Cache     CacheConfig             `yaml:"cache"`
	Scraper   ScraperConfig           `yaml:"scraper"`
	Refresher RefresherConfig         `yaml:"refresher"`
	Notifier  NotifierConfig          `yaml:"notifier"`
	Routes    map[string]RouteConfig  `yaml:"routes"`
}

// ServerConfig 是 HTTP 服务器配置。
type ServerConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	CacheTTL int    `yaml:"cache_ttl"`
}

// StorageConfig 是持久化存储配置。
type StorageConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

// CacheConfig 是缓存 TTL 配置。
type CacheConfig struct {
	FeedTTL    int `yaml:"feed_ttl"`
	ArticleTTL int `yaml:"article_ttl"`
}

// ScraperConfig 是反爬全局配置。
type ScraperConfig struct {
	Proxy       string  `yaml:"proxy"`
	RateLimit   float64 `yaml:"rate_limit"`
	Impersonate string  `yaml:"impersonate"`
}

// RefresherConfig 是后台刷新器配置。
type RefresherConfig struct {
	StartupDelay   int    `yaml:"startup_delay"`
	MaxRetries     int    `yaml:"max_retries"`
	RetryBaseDelay int    `yaml:"retry_base_delay"`
	PreinitURL     string `yaml:"preinit_url"`
}

// NotifierConfig 是通知配置。
type NotifierConfig struct {
	Enabled      bool     `yaml:"enabled"`
	ServiceURLs  []string `yaml:"service_urls"`
}

// RouteConfig 是单个路由的配置（通用字段，各路由可自行扩展）。
type RouteConfig struct {
	Enabled           bool            `yaml:"enabled"`
	Cookie            string          `yaml:"cookie"`
	RateLimit         *float64        `yaml:"rate_limit,omitempty"`
	Proxy             *string         `yaml:"proxy,omitempty"`
	Impersonate       *string         `yaml:"impersonate,omitempty"`
	PreheatOnStartup  bool            `yaml:"preheat_on_startup"`
	RefreshInterval   int             `yaml:"refresh_interval"`
	Feeds             []FeedConfig    `yaml:"feeds"`
}

// FeedConfig 是路由中单个 feed 的配置。
type FeedConfig struct {
	UserID string `yaml:"user_id"`
	Alias  string `yaml:"alias"`
	Limit  int    `yaml:"limit"`
}

// Load 从指定路径加载 YAML 配置文件并返回 Config 结构体。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}

	// 设置默认值
	setDefaults(&cfg)

	return &cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8000
	}
	if cfg.Cache.FeedTTL == 0 {
		cfg.Cache.FeedTTL = 21600
	}
	if cfg.Cache.ArticleTTL == 0 {
		cfg.Cache.ArticleTTL = 43200
	}
	if cfg.Scraper.RateLimit == 0 {
		cfg.Scraper.RateLimit = 1.0
	}
	if cfg.Scraper.Impersonate == "" {
		cfg.Scraper.Impersonate = "chrome131"
	}
	if cfg.Refresher.StartupDelay == 0 {
		cfg.Refresher.StartupDelay = 5
	}
	if cfg.Refresher.MaxRetries == 0 {
		cfg.Refresher.MaxRetries = 3
	}
	if cfg.Refresher.RetryBaseDelay == 0 {
		cfg.Refresher.RetryBaseDelay = 5
	}
}
