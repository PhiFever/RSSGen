package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/PhiFever/RSSGen/internal/cache"
	"github.com/PhiFever/RSSGen/internal/health"
	"github.com/PhiFever/RSSGen/internal/media"
	"github.com/PhiFever/RSSGen/internal/pipeline"
	"github.com/PhiFever/RSSGen/internal/refresher"
	"github.com/PhiFever/RSSGen/internal/route"
)

type feedObserver interface {
	Observe(ref pipeline.FeedRef)
}

func makeStatusHandler(ref *refresher.Refresher, archiver *media.Archiver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"enabled": ref != nil}
		if ref == nil {
			resp["message"] = "后台刷新未启用"
		} else {
			resp["feeds"] = ref.GetStatus()
		}
		if archiver != nil {
			resp["media"] = archiver.Stats()
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			logResponseWriteError("编码状态响应失败", err)
		}
	}
}

func makeRouter(
	feedHealth *health.FeedHealth,
	feedCache *cache.TTLCache,
	ref *refresher.Refresher,
	pipe *pipeline.Pipeline,
	feedCatalog feedObserver,
	archiver *media.Archiver,
) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// 不接入已弃用的 RealIP；当前服务不基于客户端 IP 做信任决策。

	// 首页：列出所有路由
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		result := route.GetDescriptions()
		w.Header().Set("Content-Type", "application/json")
		routesJSON, err := json.Marshal(result)
		if err != nil {
			slog.Error("编码路由列表失败", "error", err)
			http.Error(w, "编码路由列表失败", http.StatusInternalServerError)
			return
		}
		if _, err := fmt.Fprintf(w, `{"name":"RSSGen","routes":%s}`, routesJSON); err != nil {
			logResponseWriteError("写入首页响应失败", err)
		}
	})

	// Feed 路由：GET /feed/{route_name}/*
	r.Get("/feed/{route_name}/*", makeFeedHandler(feedHealth, feedCache, pipe, feedCatalog))

	// 状态接口
	r.Get("/status", makeStatusHandler(ref, archiver))

	return r
}

// splitPath 将路径分割为非空段。
func splitPath(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var result []string
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func makeFeedHandler(
	feedHealth *health.FeedHealth,
	feedCache *cache.TTLCache,
	pipe *pipeline.Pipeline,
	feedCatalog feedObserver,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeName := chi.URLParam(r, "route_name")
		path := chi.URLParam(r, "*")
		pathParts := splitPath(path)

		registry := route.GetRegistry()
		if _, ok := registry[routeName]; !ok {
			http.Error(w, fmt.Sprintf("路由不存在: %s", routeName), http.StatusNotFound)
			return
		}

		opts := pipeline.OptionsFromQuery(r.URL.Query())
		ref := pipe.FeedRef(routeName, pathParts, opts)

		// 检查 feed 是否被禁用
		if feedHealth.IsFeedDisabled(ref.HealthKey) {
			http.Error(w, fmt.Sprintf("订阅源 %s 已禁用（业务错误），重启后恢复", ref.HealthKey), http.StatusBadGateway)
			return
		}
		if feedCatalog != nil && pipe.RouteAllowsDynamicObservation(routeName) {
			feedCatalog.Observe(ref)
		}

		// 检查缓存
		if cached, ok := feedCache.Get(ref.CacheKey); ok {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			if _, err := w.Write([]byte(cached)); err != nil {
				logResponseWriteError("写入缓存 feed 响应失败", err, "route", routeName)
			}
			return
		}

		result, err := pipe.Refresh(routeName, pathParts, opts)
		if err != nil {
			feedHealth.RecordFailure(ref.HealthKey, err)
			slog.Error("路由抓取失败", "route", routeName, "error", err)
			http.Error(w, fmt.Sprintf("抓取失败: %v", err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		if _, err := w.Write([]byte(result.XML)); err != nil {
			logResponseWriteError("写入 feed 响应失败", err, "route", routeName)
		}
	}
}

func logResponseWriteError(message string, err error, attrs ...any) {
	attrs = append(attrs, "error", err)
	if isClientDisconnectError(err) {
		slog.Warn(message+"（客户端已断开）", attrs...)
		return
	}
	slog.Error(message, attrs...)
}

func isClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "client disconnected") ||
		strings.Contains(msg, "use of closed network connection")
}
