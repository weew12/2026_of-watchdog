// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package executor 提供函数执行相关的运行器实现 ModeInproc
// 用于在进程内直接执行函数逻辑，提供请求拦截、超时控制、日志记录等核心能力
package executor

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"github.com/openfaas/faas-provider/httputil"
)

// InprocRunner 进程内函数执行器
// 用于在当前Go进程内直接运行HTTP函数，无进程启动开销，性能最高
type InprocRunner struct {
	// handler 函数业务逻辑的HTTP处理函数
	handler http.HandlerFunc
	// prefixLogs 是否为日志添加前缀标识
	prefixLogs bool
	// logBufferSize 日志读取缓冲区大小
	logBufferSize int
	// execTimeout 函数单请求执行超时时间
	execTimeout time.Duration
	// logCallId 是否在日志中记录请求调用ID
	logCallId bool
}

// NewInprocRunner 创建并初始化一个进程内执行器实例
// handler: 函数业务处理函数
// prefixLogs: 是否开启日志前缀
// logBufferSize: 日志缓冲区大小
// logCallId: 是否记录调用ID
// execTimeout: 执行超时时间
func NewInprocRunner(handler http.HandlerFunc, prefixLogs bool, logBufferSize int, logCallId bool, execTimeout time.Duration) *InprocRunner {
	return &InprocRunner{
		handler:       handler,
		prefixLogs:    prefixLogs,
		logBufferSize: logBufferSize,
		logCallId:     logCallId,
		execTimeout:   execTimeout,
	}
}

// Start 启动执行器
// 进程内模式无需启动操作，直接返回nil
func (inpr *InprocRunner) Start() error {
	return nil
}

// Run 执行函数处理逻辑
// 接收HTTP请求，设置执行超时，拦截响应并记录请求日志
// 自动忽略kube-probe健康检查请求，避免日志刷屏
func (inpr *InprocRunner) Run(w http.ResponseWriter, r *http.Request) error {

	ctx, cancel := context.WithTimeout(r.Context(), inpr.execTimeout)
	defer cancel()

	st := time.Now()
	ww := httputil.NewHttpWriteInterceptor(w)
	inpr.handler(ww, r.WithContext(ctx))

	done := time.Since(st)
	// 排除kubelet健康检查请求的日志，避免污染日志收集系统
	if !strings.HasPrefix(r.UserAgent(), "kube-probe") {
		if inpr.logCallId {
			callId := r.Header.Get("X-Call-Id")
			if callId == "" {
				callId = "none"
			}
			// i.e POST / - 200 - ContentLength: 335B (4.8791s) [none]
			log.Printf("%s %s - %d - ContentLength: %s (%.4fs) [%s]", r.Method, r.RequestURI, ww.Status(), units.HumanSize(float64(ww.BytesWritten())), done.Seconds(), callId)
		} else {
			// i.e POST / - 200 - ContentLength: 335B (4.8791s)
			log.Printf("%s %s - %d - ContentLength: %s (%.4fs)", r.Method, r.RequestURI, ww.Status(), units.HumanSize(float64(ww.BytesWritten())), done.Seconds())
		}
	}

	return nil
}
