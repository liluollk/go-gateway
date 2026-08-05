// Package metrics 定义网关的 Prometheus 指标，并暴露 /metrics 端点。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// RequestsTotal 记录请求总数，按模型、状态码、供应商分组。
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total number of chat completion requests.",
		},
		[]string{"model", "status_code", "provider"},
	)

	// RequestDuration 记录请求耗时分布（秒），按模型、供应商分组。
	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "Histogram of request duration in seconds.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"model", "provider"},
	)

	// ErrorsTotal 记录错误总数，按模型、错误类型分组。
	ErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_errors_total",
			Help: "Total number of errors grouped by type.",
		},
		[]string{"model", "error_type"},
	)

	// TokensConsumed 记录 Token 消耗总量，按模型、Token 类型（prompt/completion）分组。
	TokensConsumed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_tokens_consumed_total",
			Help: "Total number of tokens consumed.",
		},
		[]string{"model", "token_type"},
	)

	// UpstreamHealth 记录上游供应商健康状态（1=健康, 0=不健康）。
	UpstreamHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_upstream_health",
			Help: "Health status of upstream providers (1=healthy, 0=unhealthy).",
		},
		[]string{"provider"},
	)
)

func init() {
	prometheus.MustRegister(
		RequestsTotal,
		RequestDuration,
		ErrorsTotal,
		TokensConsumed,
		UpstreamHealth,
	)
}

// Handler 返回 Prometheus 的 /metrics HTTP 处理器。
func Handler() http.Handler {
	return promhttp.Handler()
}