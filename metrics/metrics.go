// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package metrics 提供指标服务与HTTP请求监控能力
// 对外暴露Prometheus指标接口，并自动统计请求耗时、并发、状态码等数据
package metrics

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsServer 指标服务器
// 用于启动独立HTTP服务暴露Prometheus监控指标
type MetricsServer struct {
	// s 底层HTTP服务器实例
	s *http.Server
	// port 指标服务监听端口
	port int
}

// Register 注册指标服务器
// 创建并配置HTTP服务器，绑定/metrics路由
func (m *MetricsServer) Register(metricsPort int) {

	m.port = metricsPort

	readTimeout := time.Millisecond * 500
	writeTimeout := time.Millisecond * 500

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())

	m.s = &http.Server{
		Addr:           fmt.Sprintf(":%d", metricsPort),
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
		MaxHeaderBytes: 1 << 20, // 最大请求头 1MB
		Handler:        metricsMux,
	}

}

// Serve 启动指标服务（非阻塞）
// 在协程中运行服务器，并监听关闭信号实现优雅停机
func (m *MetricsServer) Serve(cancel chan bool) {
	log.Printf("Metrics listening on port: %d\n", m.port)

	go func() {
		if err := m.s.ListenAndServe(); err != http.ErrServerClosed {
			panic(fmt.Sprintf("metrics error ListenAndServe: %v\n", err))
		}
	}()

	go func() {
		select {
		case <-cancel:
			log.Printf("metrics server shutdown\n")

			m.s.Shutdown(context.Background())
		}
	}()
}

// InstrumentHandler HTTP请求指标埋点包装器
// 包装原始处理器，自动统计请求计数、耗时、并发数
func InstrumentHandler(next http.Handler, _http Http) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		then := promhttp.InstrumentHandlerCounter(_http.RequestsTotal,
			promhttp.InstrumentHandlerDuration(_http.RequestDurationHistogram, next))

		_http.InFlight.Inc()
		defer _http.InFlight.Dec()

		then(w, r)
	}
}
