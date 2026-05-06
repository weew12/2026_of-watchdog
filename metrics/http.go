// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package metrics 提供HTTP服务监控指标实现
// 基于Prometheus采集请求总数、耗时、并发量等监控数据
package metrics

import (
	"sync/atomic"
	"time"

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

	// weew12 新增：watchdog ready 时间戳
	// 表示当前函数 Pod 内 watchdog 首次进入可接受业务请求状态的 Unix 时间戳，单位：秒
	WatchdogReadyTimestamp prometheus.Gauge

	// weew12 新增：首个真实业务请求处理时长直方图
	// 仅在当前函数 Pod 生命周期内的首个真实业务请求完成时记录一次
	FirstRequestDurationHistogram prometheus.Histogram

	// weew12 新增：首请求样本是否已经记录
	firstRequestRecorded uint32
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
		// weew12 新增：watchdog ready 时间戳
		WatchdogReadyTimestamp: promauto.NewGauge(prometheus.GaugeOpts{
			Subsystem: "watchdog",
			Name:      "ready_timestamp_seconds",
			Help:      "Unix timestamp when watchdog first becomes ready to accept function requests.",
		}),
		// weew12 新增：首个真实业务请求处理时长
		FirstRequestDurationHistogram: promauto.NewHistogram(prometheus.HistogramOpts{
			Subsystem: "watchdog",
			Name:      "first_request_duration_seconds",
			Help:      "Seconds spent serving the first real function request in the current watchdog instance.",
			Buckets:   prometheus.DefBuckets,
		}),
	}

	// 优雅停机期间查询指标时默认返回0
	h.InFlight.Set(0)
	return h
}

// RecordWatchdogReady 记录 watchdog 首次进入 ready 状态的时间戳
// 该方法应在 watchdog 完成初始化并开始接受业务请求时调用
func (h *Http) RecordWatchdogReady() {
	h.WatchdogReadyTimestamp.Set(float64(time.Now().UnixNano()) / 1e9)
}

// TryMarkFirstRequest 尝试标记当前请求为该 Pod 生命周期内的首个真实业务请求
func (h *Http) TryMarkFirstRequest() bool {
	return atomic.CompareAndSwapUint32(&h.firstRequestRecorded, 0, 1)
}

// ObserveFirstRequestDuration 记录首个真实业务请求处理时长
func (h *Http) ObserveFirstRequestDuration(duration time.Duration) {
	h.FirstRequestDurationHistogram.Observe(duration.Seconds())
}
