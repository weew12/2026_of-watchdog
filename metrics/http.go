// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package metrics 提供HTTP服务监控指标实现
// 基于Prometheus采集请求总数、耗时、并发量等监控数据
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Http HTTP服务监控指标集合
// 包含请求总数、请求耗时直方图、当前并发请求数三类核心指标
type Http struct {
	// RequestsTotal 累计处理的HTTP请求总数
	// 按状态码、请求方法分类统计
	RequestsTotal *prometheus.CounterVec

	// RequestDurationHistogram HTTP请求处理耗时分布直方图
	// 按状态码、请求方法分类统计
	RequestDurationHistogram *prometheus.HistogramVec

	// InFlight 当前正在处理中的HTTP请求数量
	InFlight prometheus.Gauge
}

// NewHttp 创建并初始化HTTP监控指标实例
// 自动注册指标到Prometheus，并将并发请求数初始化为0
func NewHttp() Http {
	h := Http{
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "total HTTP requests processed",
		}, []string{"code", "method"}),
		RequestDurationHistogram: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Seconds spent serving HTTP requests.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"code", "method"}),
		InFlight: promauto.NewGauge(prometheus.GaugeOpts{
			Subsystem: "http",
			Name:      "requests_in_flight",
			Help:      "total HTTP requests in-flight",
		}),
	}

	// 优雅停机期间查询指标时默认返回0
	h.InFlight.Set(0)
	return h
}
