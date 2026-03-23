// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package executor 提供函数执行器实现 ModeHTTP
// 包含HTTP代理模式、进程内执行模式的函数运行与日志管理功能
package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	units "github.com/docker/go-units"
	fhttputil "github.com/openfaas/faas-provider/httputil"
)

// HTTPFunctionRunner HTTP函数运行器
// 创建并维护一个常驻进程，负责处理所有函数调用请求
type HTTPFunctionRunner struct {
	// ExecTimeout 上游函数调用的最大执行超时时间
	ExecTimeout time.Duration
	// ReadTimeout HTTP服务器读取请求超时时间
	ReadTimeout time.Duration
	// WriteTimeout HTTP服务器写入响应超时时间
	WriteTimeout time.Duration
	// Process 要执行的函数进程路径
	Process string
	// ProcessArgs 传递给进程的参数列表
	ProcessArgs []string
	// Command 执行的系统命令实例 = Process + ProcessArgs
	Command *exec.Cmd
	// StdinPipe 进程标准输入管道
	StdinPipe io.WriteCloser
	// StdoutPipe 进程标准输出管道
	StdoutPipe io.ReadCloser
	// Client 发起上游HTTP请求的客户端
	Client *http.Client
	// UpstreamURL 上游服务地址
	UpstreamURL *url.URL
	// BufferHTTPBody 是否缓冲HTTP请求体
	BufferHTTPBody bool
	// LogPrefix 是否为日志添加前缀
	LogPrefix bool
	// LogBufferSize 日志缓冲区大小
	LogBufferSize int
	// LogCallId 是否记录请求调用ID
	LogCallId bool
	// ReverseProxy 标准库反向代理实例
	ReverseProxy *httputil.ReverseProxy
}

// Start 启动用于处理请求的子进程
// 创建进程管道、绑定日志、监听系统信号并启动进程
func (f *HTTPFunctionRunner) Start() error {
	cmd := exec.Command(f.Process, f.ProcessArgs...)

	var stdinErr error
	var stdoutErr error

	f.Command = cmd
	// 创建进程管道
	// 用于将子进程的标准输入、输出、错误重定向到当前进程
	f.StdinPipe, stdinErr = cmd.StdinPipe()
	if stdinErr != nil {
		return stdinErr
	}

	f.StdoutPipe, stdoutErr = cmd.StdoutPipe()
	if stdoutErr != nil {
		return stdoutErr
	}

	errPipe, _ := cmd.StderrPipe()

	// 将进程标准输出/错误重定向并打印到当前进程
	bindLoggingPipe("stderr", errPipe, os.Stderr, f.LogPrefix, f.LogBufferSize)
	bindLoggingPipe("stdout", f.StdoutPipe, os.Stdout, f.LogPrefix, f.LogBufferSize)

	f.Client = makeProxyClient(f.ExecTimeout)

	// 监听SIGTERM信号，优雅关闭子进程
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)

		<-sig
		cmd.Process.Signal(syscall.SIGTERM)
	}()

	err := cmd.Start()
	// 监听子进程退出状态，异常则直接退出
	go func() {
		err := cmd.Wait()
		if err != nil {
			log.Fatalf("Forked function has terminated: %s", err.Error())
		}
	}()

	return err
}

// Run 运行基于HTTP协议的常驻进程函数
// 处理请求转发、超时控制、响应复制与请求日志记录
func (f *HTTPFunctionRunner) Run(req FunctionRequest, contentLength int64, r *http.Request, w http.ResponseWriter) error {
	startedTime := time.Now()

	upstreamURL := f.UpstreamURL.String()

	if len(r.RequestURI) > 0 {
		upstreamURL += r.RequestURI
	}

	body := r.Body

	// 开启缓冲则读取全部请求体，兼容不支持分块编码的服务
	if f.BufferHTTPBody {
		reqBody, _ := io.ReadAll(r.Body)
		body = io.NopCloser(bytes.NewReader(reqBody))
	}

	request, err := http.NewRequest(r.Method, upstreamURL, body)
	if err != nil {
		return err
	}

	// 复制请求头
	for h := range r.Header {
		request.Header.Set(h, r.Header.Get(h))
	}

	request.Host = r.Host
	copyHeaders(request.Header, &r.Header)

	// 获取执行超时时间
	execTimeout := getTimeout(r, f.ExecTimeout)

	var reqCtx context.Context
	var cancel context.CancelFunc

	if execTimeout.Nanoseconds() > 0 {
		reqCtx, cancel = context.WithTimeout(r.Context(), execTimeout)
	} else {
		reqCtx = r.Context()
		cancel = func() {}
	}
	defer cancel()

	// 流式/WebSocket请求使用标准库反向代理
	if requiresStdlibProxy(r) {
		ww := fhttputil.NewHttpWriteInterceptor(w)

		f.ReverseProxy.ServeHTTP(w, r)
		done := time.Since(startedTime)

		log.Printf("%s %s - %d - Bytes: %s (%.4fs)", r.Method, r.RequestURI, ww.Status(), units.HumanSize(float64(ww.BytesWritten())), done.Seconds())
	} else {
		// 普通HTTP请求直接调用上游服务
		res, err := f.Client.Do(request.WithContext(reqCtx))
		if err != nil {
			log.Printf("Upstream HTTP request error: %s\n", err.Error())

			// 与上下文/超时无关的错误
			if reqCtx.Err() == nil {
				w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", time.Since(startedTime).Seconds()))
				w.Header().Add("X-OpenFaaS-Internal", "of-watchdog")

				w.WriteHeader(http.StatusInternalServerError)

				return nil
			}

			<-reqCtx.Done()

			// 超时导致的错误
			if reqCtx.Err() != nil {
				log.Printf("Upstream HTTP killed due to exec_timeout: %s\n", f.ExecTimeout)
				w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", time.Since(startedTime).Seconds()))
				w.Header().Add("X-OpenFaaS-Internal", "of-watchdog")

				w.WriteHeader(http.StatusGatewayTimeout)
				return nil
			}

			w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", time.Since(startedTime).Seconds()))
			w.Header().Add("X-OpenFaaS-Internal", "of-watchdog")

			w.WriteHeader(http.StatusInternalServerError)
			return err
		}

		// 复制响应头与状态码
		copyHeaders(w.Header(), &res.Header)
		done := time.Since(startedTime)

		w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", done.Seconds()))

		w.WriteHeader(res.StatusCode)
		if res.Body != nil {
			defer res.Body.Close()

			if _, err := io.Copy(w, res.Body); err != nil {
				log.Printf("Error copying response body: %s", err)
			}
		}

		// 忽略kubelet健康检查日志，避免刷屏
		if !strings.HasPrefix(r.UserAgent(), "kube-probe") {
			if f.LogCallId {
				callId := r.Header.Get("X-Call-Id")
				if callId == "" {
					callId = "none"
				}

				log.Printf("%s %s - %s - ContentLength: %s (%.4fs) [%s]", r.Method, r.RequestURI, res.Status, units.HumanSize(float64(res.ContentLength)), done.Seconds(), callId)
			} else {
				log.Printf("%s %s - %s - ContentLength: %s (%.4fs)", r.Method, r.RequestURI, res.Status, units.HumanSize(float64(res.ContentLength)), done.Seconds())
			}
		}
	}

	return nil
}

// getTimeout 获取请求执行超时时间
// 优先使用请求头X-Timeout，不超过默认最大超时
func getTimeout(r *http.Request, defaultTimeout time.Duration) time.Duration {
	execTimeout := defaultTimeout
	if v := r.Header.Get("X-Timeout"); len(v) > 0 {
		dur, err := time.ParseDuration(v)
		if err == nil {
			if dur <= defaultTimeout {
				execTimeout = dur
			}
		}
	}

	return execTimeout
}

// copyHeaders 深度复制HTTP请求头
// 避免源header被意外修改
func copyHeaders(destination http.Header, source *http.Header) {
	for k, v := range *source {
		vClone := make([]string, len(v))
		copy(vClone, v)
		(destination)[k] = vClone
	}
}

// makeProxyClient 创建HTTP代理客户端
// 配置长连接、超时、重定向策略
func makeProxyClient(dialTimeout time.Duration) *http.Client {
	proxyClient := http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: 10 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			DisableKeepAlives:     false,
			IdleConnTimeout:       500 * time.Millisecond,
			ExpectContinueTimeout: 1500 * time.Millisecond,
		},
		// 禁止自动重定向
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &proxyClient
}

// requiresStdlibProxy 判断是否需要使用标准库反向代理
// 支持SSE、流式JSON、WebSocket等特殊协议
func requiresStdlibProxy(req *http.Request) bool {
	acceptHeader := strings.ToLower(req.Header.Get("Accept"))

	return strings.Contains(acceptHeader, "text/event-stream") ||
		strings.Contains(acceptHeader, "application/x-ndjson") ||
		req.Header.Get("Upgrade") == "websocket"
}
