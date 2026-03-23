// Package pkg 提供了 OpenFaaS of-watchdog 的核心实现。
//
// of-watchdog 是 OpenFaaS 函数的 HTTP 服务器守护进程，负责：
//   - 接收 HTTP 请求并转发给函数进程
//   - 管理函数的生命周期和健康检查
//   - 支持多种运行模式（streaming、serializing、http、static、inproc）
//   - 提供并发限制、JWT 认证等中间件功能
//   - 收集和暴露 Prometheus 指标
//
// 主要组件：
//   - Watchdog: 主结构体，封装配置和启动逻辑
//   - 请求处理器: 根据不同模式处理 HTTP 请求
//   - 健康检查: 提供 /_/health 和 /_/ready 端点
//   - 指标收集: 收集请求指标并暴露给 Prometheus
package pkg

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/docker/go-units"
	"github.com/openfaas/faas-middleware/auth"
	limiter "github.com/openfaas/faas-middleware/concurrency-limiter"
	"github.com/openfaas/of-watchdog/config"
	"github.com/openfaas/of-watchdog/executor"
	"github.com/openfaas/of-watchdog/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var (
	// acceptingConnections 是一个原子计数器，用于标识服务是否正在接受新连接
	// 值为 1 表示服务正常接受连接，值为 0 表示服务正在关闭或不可用
	// 该变量被健康检查端点 /_/health 和 /_/ready 使用
	acceptingConnections int32
)

// Watchdog 是 HTTP 服务器守护进程的核心结构体
// 它封装了 OpenFaaS 函数代理的所有配置和运行时状态
type Watchdog struct {
	// config 包含所有运行时配置选项，如端口、超时、运行模式等
	config config.WatchdogConfig
	// shutdownCtx 用于优雅关闭时的上下文控制
	shutdownCtx context.Context
}

// NewWatchdog 创建一个新的 Watchdog 实例
// 参数 config 包含所有必要的配置信息
// 返回初始化后的 Watchdog 指针
func NewWatchdog(config config.WatchdogConfig) *Watchdog {
	return &Watchdog{
		config: config,
	}
}

// Start 启动 Watchdog HTTP 服务器
// 这是 Watchdog 的主入口方法，负责：
//  1. 构建请求处理链（包含认证、并发限制等中间件）
//  2. 注册 HTTP 路由（主路由、健康检查、就绪检查）
//  3. 启动指标收集服务器
//  4. 启动主 HTTP 服务器并监听关闭信号
//
// 参数 ctx 用于控制优雅关闭
// 返回错误信息，正常情况下返回 nil
func (w *Watchdog) Start(ctx context.Context) error {

	// 初始化连接状态为不接受新连接，直到锁文件创建成功
	atomic.StoreInt32(&acceptingConnections, 0)

	// 构建基础函数处理器，不包含任何中间件
	// 用于就绪检查，确保检查的是原始处理器而非被限流包装的处理器
	baseFunctionHandler := buildRequestHandler(w.config, w.config.PrefixLogs)
	requestHandler := baseFunctionHandler

	// 如果启用了 JWT 认证，将认证中间件包装到处理器链中
	if w.config.JWTAuthentication {
		handler, err := makeJWTAuthHandler(w.config, baseFunctionHandler)
		if err != nil {
			return fmt.Errorf("error creating JWTAuthMiddleware: %w", err)
		}

		requestHandler = handler
	}

	// 如果配置了最大并发请求数，创建并发限制器
	var limit limiter.Limiter
	if w.config.MaxInflight > 0 {
		requestLimiter := limiter.NewConcurrencyLimiter(requestHandler, w.config.MaxInflight)
		requestHandler = requestLimiter.Handler()
		limit = requestLimiter
	}

	// 打印运行模式和函数进程信息
	log.Printf("Watchdog mode: %s\tfprocess: %q\n", config.WatchdogMode(w.config.OperationalMode), w.config.FunctionProcess)

	// 初始化 HTTP 指标收集器
	httpMetrics := metrics.NewHttp()
	// 注册主路由，并包装指标收集
	http.HandleFunc("/", metrics.InstrumentHandler(requestHandler, httpMetrics))
	// 注册健康检查端点
	http.HandleFunc("/_/health", makeHealthHandler(w.LockFilePresent))
	// 注册就绪检查端点
	http.Handle("/_/ready", &readiness{
		// 使用原始处理器，而非被限流包装的处理器
		functionHandler: baseFunctionHandler,
		endpoint:        w.config.ReadyEndpoint,
		lockCheck:       w.LockFilePresent,
		limiter:         limit,
	})

	// 创建并注册指标服务器
	metricsServer := metrics.MetricsServer{}
	metricsServer.Register(w.config.MetricsPort)

	// 用于停止指标服务器的通道
	cancel := make(chan bool)

	// 在后台启动指标服务器
	go metricsServer.Serve(cancel)

	// 创建主 HTTP 服务器
	s := &http.Server{
		Addr:           fmt.Sprintf(":%d", w.config.TCPPort),
		ReadTimeout:    w.config.HTTPReadTimeout,
		WriteTimeout:   w.config.HTTPWriteTimeout,
		MaxHeaderBytes: 1 << 20, // 最大请求头大小 1MB
	}

	// 打印超时配置信息
	log.Printf("Timeouts: read: %s write: %s hard: %s health: %s\n",
		w.config.HTTPReadTimeout,
		w.config.HTTPWriteTimeout,
		w.config.ExecTimeout,
		w.config.HealthcheckInterval)

	// 如果启用了 JWT 认证，打印认证状态
	if w.config.JWTAuthentication {
		log.Printf("JWT Auth: %v\n", w.config.JWTAuthentication)
	}

	// 打印监听端口
	log.Printf("Listening on port: %d\n", w.config.TCPPort)

	// 启动服务器并监听关闭信号
	listenUntilShutdown(s,
		ctx,
		w.config.HealthcheckInterval,
		w.config.HTTPWriteTimeout,
		w.config.SuppressLock,
		&httpMetrics)

	return nil
}

// markUnhealthy 将服务标记为不健康状态
// 该函数在服务关闭时被调用，执行以下操作：
//   - 将 acceptingConnections 设置为 0，表示不再接受新连接
//   - 删除锁文件，通知外部健康检查器服务已关闭
//
// 返回删除锁文件时可能发生的错误
func markUnhealthy() error {
	// 原子操作：设置连接状态为不接受
	atomic.StoreInt32(&acceptingConnections, 0)

	// 删除锁文件
	path := filepath.Join(os.TempDir(), ".lock")
	log.Printf("Removing lock-file : %s\n", path)
	removeErr := os.Remove(path)
	return removeErr
}

// listenUntilShutdown 启动 HTTP 服务器并处理优雅关闭
// 该函数实现了完整的关闭流程：
//  1. 监听 SIGTERM 信号或上下文取消
//  2. 标记服务为不健康
//  3. 等待健康检查间隔
//  4. 优雅关闭 HTTP 服务器
//
// 参数：
//   - s: HTTP 服务器实例
//   - shutdownCtx: 用于控制关闭的上下文
//   - healthcheckInterval: 健康检查间隔时间
//   - writeTimeout: 写超时时间，也用作最大关闭等待时间
//   - suppressLock: 是否禁用锁文件机制
//   - httpMetrics: HTTP 指标收集器，用于获取当前连接数
func listenUntilShutdown(s *http.Server, shutdownCtx context.Context, healthcheckInterval time.Duration, writeTimeout time.Duration, suppressLock bool, httpMetrics *metrics.Http) error {

	// 用于通知所有连接已关闭的通道
	idleConnsClosed := make(chan struct{})

	// 启动信号监听协程，处理关闭逻辑
	go func() {
		// 创建信号通道，监听 SIGTERM
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)

		reason := ""

		// 等待关闭信号或上下文取消
		select {
		case <-sig:
			reason = "SIGTERM"
		case <-shutdownCtx.Done():
			reason = "Context cancelled"
		}

		log.Printf("%s: no new connections in %s\n", reason, healthcheckInterval.String())

		// 标记服务为不健康状态
		if err := markUnhealthy(); err != nil {
			log.Printf("Unable to mark server as unhealthy: %s\n", err.Error())
		}

		// 等待健康检查间隔，让负载均衡器有时间感知服务已下线
		<-time.Tick(healthcheckInterval)

		// 获取当前正在处理的请求数
		connections := int64(testutil.ToFloat64(httpMetrics.InFlight))
		log.Printf("No new connections allowed, draining: %d requests\n", connections)

		// 创建带超时的上下文，最大等待时间为 writeTimeout
		// 这确保了关闭过程不会无限期等待
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()

		// 优雅关闭 HTTP 服务器
		if err := s.Shutdown(ctx); err != nil {
			log.Printf("Error in Shutdown: %v", err)
		}

		// 打印关闭后的剩余连接数
		connections = int64(testutil.ToFloat64(httpMetrics.InFlight))
		log.Printf("Exiting. Active connections: %d\n", connections)

		// 通知主协程关闭完成
		close(idleConnsClosed)
	}()

	// 在单独的协程中启动 HTTP 服务器
	go func() {
		if err := s.ListenAndServe(); err != http.ErrServerClosed {
			// 非正常关闭时记录错误
			log.Printf("Error ListenAndServe: %v", err)
			close(idleConnsClosed)
		}
	}()

	// 处理锁文件创建
	if suppressLock == false {
		// 创建锁文件，表示服务已就绪
		path, writeErr := createLockFile()

		if writeErr != nil {
			return fmt.Errorf("cannot write %s. To disable lock-file set env suppress_lock=true: %w", path, writeErr)
		}
	} else {
		// 警告：禁用锁文件意味着没有自动健康检查
		log.Println("Warning: \"suppress_lock\" is enabled. No automated health-checks will be in place for your function.")

		// 直接标记为接受连接
		atomic.StoreInt32(&acceptingConnections, 1)
	}

	// 等待服务器完全关闭
	<-idleConnsClosed

	return nil
}

// buildRequestHandler 根据配置的运行模式构建对应的请求处理器
// 该函数是请求处理链的起点，根据不同的运行模式返回不同的处理器：
//   - ModeStreaming: 流式处理模式，实时传输数据
//   - ModeSerializing: 序列化模式，等待函数执行完成后返回
//   - ModeHTTP: HTTP 代理模式，将请求代理到上游 HTTP 服务
//   - ModeStatic: 静态文件服务模式
//   - ModeInproc: 进程内处理模式
//
// 参数：
//   - cfg: Watchdog 配置
//   - prefixLogs: 是否在日志前添加前缀
//
// 返回对应的 HTTP 处理器
func buildRequestHandler(cfg config.WatchdogConfig, prefixLogs bool) http.Handler {
	var requestHandler http.HandlerFunc

	// 根据运行模式选择对应的处理器
	switch cfg.OperationalMode {
	case config.ModeStreaming:
		// 流式模式：实时传输请求和响应数据
		requestHandler = makeStreamingRequestHandler(cfg, prefixLogs, cfg.LogBufferSize)
	case config.ModeSerializing:
		// 序列化模式：等待函数完成后再返回响应
		requestHandler = makeSerializingForkRequestHandler(cfg, prefixLogs)
	case config.ModeHTTP:
		// HTTP 模式：代理请求到上游 HTTP 服务
		requestHandler = makeHTTPRequestHandler(cfg, prefixLogs, cfg.LogBufferSize)
	case config.ModeStatic:
		// 静态模式：提供静态文件服务
		requestHandler = makeStaticRequestHandler(cfg)
	case config.ModeInproc:
		// 进程内模式：在当前进程中直接调用函数处理器
		requestHandler = makeInprocRequestHandler(cfg, prefixLogs, cfg.LogBufferSize)
	default:
		// 未知模式，触发 panic
		log.Panicf("unknown watchdog mode: %d", cfg.OperationalMode)
	}

	return requestHandler
}

// createLockFile 创建锁文件并标记服务为接受连接状态
// 锁文件用于健康检查机制，表示服务已准备就绪
// 返回锁文件路径和可能的错误
func createLockFile() (string, error) {
	// 锁文件路径：系统临时目录下的 .lock 文件
	path := filepath.Join(os.TempDir(), ".lock")
	log.Printf("Writing lock-file to: %s\n", path)

	// 确保临时目录存在
	if err := os.MkdirAll(os.TempDir(), os.ModePerm); err != nil {
		return path, err
	}

	// 创建空的锁文件
	if err := os.WriteFile(path, []byte{}, 0660); err != nil {
		return path, err
	}

	// 标记服务为接受连接状态
	atomic.StoreInt32(&acceptingConnections, 1)
	return path, nil
}

// makeSerializingForkRequestHandler 创建序列化模式的请求处理器
// 序列化模式的工作方式：
//  1. 接收完整的 HTTP 请求
//  2. 启动函数进程，将请求体作为标准输入
//  3. 等待函数进程完成
//  4. 将函数进程的标准输出作为 HTTP 响应返回
//
// 这种模式适合处理可以完整缓存的请求，但不适合流式或大文件场景
// 参数：
//   - cfg: Watchdog 配置
//   - logPrefix: 是否在日志前添加前缀
//
// 返回 HTTP 处理函数
func makeSerializingForkRequestHandler(cfg config.WatchdogConfig, logPrefix bool) func(http.ResponseWriter, *http.Request) {
	// 创建序列化函数执行器
	functionInvoker := executor.SerializingForkFunctionRunner{
		ExecTimeout:   cfg.ExecTimeout,
		LogPrefix:     logPrefix,
		LogBufferSize: cfg.LogBufferSize,
	}

	return func(w http.ResponseWriter, r *http.Request) {

		var environment []string

		// 如果启用了 CGI 头注入，将 HTTP 头转换为环境变量
		if cfg.InjectCGIHeaders {
			environment = getEnvironment(r)
		}

		// 获取命令名称和参数
		commandName, arguments := cfg.Process()
		// 构建函数请求对象
		req := executor.FunctionRequest{
			Process:       commandName,
			ProcessArgs:   arguments,
			InputReader:   r.Body,
			ContentLength: &r.ContentLength,
			OutputWriter:  w,
			Environment:   environment,
			RequestURI:    r.RequestURI,
			Method:        r.Method,
			UserAgent:     r.UserAgent(),
		}

		// 设置响应内容类型
		w.Header().Set("Content-Type", cfg.ContentType)
		// 执行函数并处理错误
		err := functionInvoker.Run(req, w)
		if err != nil {
			// log.Println(err)
			log.Printf("%s", fmt.Sprintf(
				`SerializingForkFunctionRunner error: %s`,
				err.Error(),
			))
		}
	}
}

// makeStreamingRequestHandler 创建流式模式的请求处理器
// 流式模式的工作方式：
//  1. 实时将 HTTP 请求体传输给函数进程的标准输入
//  2. 实时将函数进程的标准输出传输给 HTTP 响应
//  3. 支持双向流式传输，适合大文件或长时间运行的处理
//
// 这种模式适合需要流式处理的场景，如视频转码、大文件处理等
// 参数：
//   - cfg: Watchdog 配置
//   - prefixLogs: 是否在日志前添加前缀
//   - logBufferSize: 日志缓冲区大小
//
// 返回 HTTP 处理函数
func makeStreamingRequestHandler(cfg config.WatchdogConfig, prefixLogs bool, logBufferSize int) func(http.ResponseWriter, *http.Request) {
	// 创建流式函数执行器
	functionInvoker := executor.StreamingFunctionRunner{
		ExecTimeout:   cfg.ExecTimeout,
		LogPrefix:     prefixLogs,
		LogBufferSize: logBufferSize,
	}

	return func(w http.ResponseWriter, r *http.Request) {

		var environment []string

		// 如果启用了 CGI 头注入，将 HTTP 头转换为环境变量
		if cfg.InjectCGIHeaders {
			environment = getEnvironment(r)
		}

		// 创建写入计数器，用于统计响应大小
		ww := WriterCounter{}
		ww.setWriter(w)
		// 记录请求开始时间
		start := time.Now()
		// 获取命令名称和参数
		commandName, arguments := cfg.Process()
		// 构建函数请求对象
		req := executor.FunctionRequest{
			Process:      commandName,
			ProcessArgs:  arguments,
			InputReader:  r.Body,
			OutputWriter: &ww,
			Environment:  environment,
			RequestURI:   r.RequestURI,
			Method:       r.Method,
			UserAgent:    r.UserAgent(),
		}

		// 设置响应内容类型
		w.Header().Set("Content-Type", cfg.ContentType)
		// 执行函数
		err := functionInvoker.Run(req)
		if err != nil {
			log.Printf("%s", fmt.Sprintf(
				`StreamingFunctionRunner error: %s`,
				err.Error(),
			))

			// 计算请求处理时间
			done := time.Since(start)
			// 忽略 kube-probe 的健康检查请求日志
			if !strings.HasPrefix(req.UserAgent, "kube-probe") {
				log.Printf("%s %s - %d - ContentLength: %s (%.4fs)", req.Method, req.RequestURI, http.StatusInternalServerError, units.HumanSize(float64(ww.Bytes())), done.Seconds())
				return
			}
		}

		// 计算请求处理时间
		done := time.Since(start)
		// 忽略 kube-probe 的健康检查请求日志
		if !strings.HasPrefix(req.UserAgent, "kube-probe") {
			log.Printf("%s %s - %d - ContentLength: %s (%.4fs)", req.Method, req.RequestURI, http.StatusOK, units.HumanSize(float64(ww.Bytes())), done.Seconds())
		}
	}
}

// getEnvironment 从 HTTP 请求中提取环境变量
// 该函数将 HTTP 请求的各种属性转换为环境变量，供函数进程使用
// 转换规则eg：
//   - HTTP 头: Http_HeaderName=HeaderValue（头名称中的 - 替换为 _）
//   - 请求方法: Http_Method=GET
//   - 查询参数: Http_Query=原始查询字符串
//   - 请求路径: Http_Path=/path/to/resource
//   - 传输编码: Http_Transfer_Encoding=chunked
//
// 参数 r 是 HTTP 请求对象
// 返回包含所有环境变量的字符串切片
func getEnvironment(r *http.Request) []string {
	var envs []string

	// 获取当前进程的所有环境变量作为基础
	envs = os.Environ()
	// 将 HTTP 头转换为环境变量
	for k, v := range r.Header {
		// 将头名称中的 - 替换为 _，并添加 Http_ 前缀
		kv := fmt.Sprintf("Http_%s=%s", strings.Replace(k, "-", "_", -1), v[0])
		envs = append(envs, kv)
	}
	// 添加请求方法环境变量
	envs = append(envs, fmt.Sprintf("Http_Method=%s", r.Method))

	// 如果有查询参数，添加查询字符串环境变量
	if len(r.URL.RawQuery) > 0 {
		envs = append(envs, fmt.Sprintf("Http_Query=%s", r.URL.RawQuery))
	}

	// 如果有请求路径，添加路径环境变量
	if len(r.URL.Path) > 0 {
		envs = append(envs, fmt.Sprintf("Http_Path=%s", r.URL.Path))
	}

	// 如果有传输编码，添加传输编码环境变量
	if len(r.TransferEncoding) > 0 {
		envs = append(envs, fmt.Sprintf("Http_Transfer_Encoding=%s", r.TransferEncoding[0]))
	}

	return envs
}

// makeInprocRequestHandler 创建进程内模式的请求处理器
// 进程内模式的工作方式：
//  1. 不启动外部进程，直接在当前进程中调用函数处理器
//  2. 减少了进程启动开销，适合高性能场景
//  3. 函数处理器通过 cfg.Handler 配置
//
// 这种模式适合需要极低延迟的场景，但要求函数是线程安全的
// 参数：
//   - cfg: Watchdog 配置
//   - prefixLogs: 是否在日志前添加前缀
//   - logBufferSize: 日志缓冲区大小
//
// 返回 HTTP 处理函数
func makeInprocRequestHandler(cfg config.WatchdogConfig, prefixLogs bool, logBufferSize int) func(http.ResponseWriter, *http.Request) {
	// 创建进程内运行器
	runner := executor.NewInprocRunner(cfg.Handler,
		prefixLogs,
		logBufferSize,
		cfg.LogCallId,
		cfg.ExecTimeout,
	)

	// 启动运行器
	if err := runner.Start(); err != nil {
		log.Fatalf("Failed to start in-process runner: %v", err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// 直接在当前进程中运行函数处理器
		runner.Run(w, r)
	}
}

// makeHTTPRequestHandler 创建 HTTP 代理模式的请求处理器
// HTTP 代理模式的工作方式：
//  1. 启动一个函数进程作为上游 HTTP 服务
//  2. 使用反向代理将请求转发到上游服务
//  3. 支持流式请求和响应
//
// 这种模式适合需要 HTTP 语义的场景，如 REST API 服务
// 参数：
//   - cfg: Watchdog 配置
//   - prefixLogs: 是否在日志前添加前缀
//   - logBufferSize: 日志缓冲区大小
//
// 返回 HTTP 处理函数
func makeHTTPRequestHandler(cfg config.WatchdogConfig, prefixLogs bool, logBufferSize int) func(http.ResponseWriter, *http.Request) {
	// 解析上游 URL
	upstreamURL, _ := url.Parse(cfg.UpstreamURL)

	// 获取命令名称和参数
	commandName, arguments := cfg.Process()
	// 创建 HTTP 函数执行器
	functionInvoker := executor.HTTPFunctionRunner{
		ExecTimeout:    cfg.ExecTimeout,
		Process:        commandName,
		ProcessArgs:    arguments,
		BufferHTTPBody: cfg.BufferHTTPBody,
		LogPrefix:      prefixLogs,
		LogBufferSize:  logBufferSize,
		LogCallId:      cfg.LogCallId,
		// 创建反向代理
		ReverseProxy: &httputil.ReverseProxy{
			// 请求修改器：设置上游服务地址
			Director: func(req *http.Request) {
				req.URL.Host = upstreamURL.Host
				req.URL.Scheme = "http"
			},
			// 错误处理器：空实现，错误由 Run 方法处理
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			},
			// 禁用错误日志输出
			ErrorLog: log.New(io.Discard, "", 0),
		},
	}

	// 验证上游 URL 配置
	if len(cfg.UpstreamURL) == 0 {
		log.Fatal(`For "mode=http" you must specify a valid URL for "http_upstream_url"`)
	}

	// 解析并设置上游 URL
	urlValue, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		log.Fatalf(`For "mode=http" you must specify a valid URL for "http_upstream_url", error: %s`, err)
	}

	functionInvoker.UpstreamURL = urlValue

	// 启动函数进程
	log.Printf("Forking: %s, arguments: %s", commandName, arguments)
	functionInvoker.Start()

	return func(w http.ResponseWriter, r *http.Request) {

		// 构建函数请求对象
		req := executor.FunctionRequest{
			Process:      commandName,
			ProcessArgs:  arguments,
			OutputWriter: w,
		}

		// 确保请求体在处理完成后关闭
		if r.Body != nil {
			defer r.Body.Close()
		}

		// 执行请求代理
		if err := functionInvoker.Run(req, r.ContentLength, r, w); err != nil {
			// 返回 500 错误
			w.WriteHeader(500)
			// w.Write([]byte(err.Error()))
			log.Printf("%s", fmt.Sprintf(
				`HTTPFunctionRunner error: %s`,
				err.Error(),
			))
			w.Write([]byte(fmt.Sprintf(
				`HTTPFunctionRunner error: %s`,
				err.Error(),
			)))
		}
	}
}

// makeStaticRequestHandler 创建静态文件服务模式的请求处理器
// 静态文件服务模式的工作方式：
//  1. 直接提供指定目录下的静态文件
//  2. 不启动任何函数进程
//  3. 使用标准库的 http.FileServer 实现
//
// 这种模式适合提供静态网站、文档等静态资源
// 参数 cfg 是 Watchdog 配置，必须包含 static_path 配置
// 返回 HTTP 处理函数
func makeStaticRequestHandler(cfg config.WatchdogConfig) http.HandlerFunc {
	// 验证静态文件路径配置
	if cfg.StaticPath == "" {
		log.Fatal(`For mode=static you must specify the "static_path" to serve`)
	}

	// 打印静态文件服务路径
	log.Printf("Serving files at: %s", cfg.StaticPath)
	// 返回文件服务器处理器
	return http.FileServer(http.Dir(cfg.StaticPath)).ServeHTTP
}

// LockFilePresent 检查锁文件是否存在
// 这是 Watchdog 结构体的方法，用于健康检查
// 返回 true 表示锁文件存在，服务已就绪
func (w *Watchdog) LockFilePresent() bool {
	return lockFilePresent()
}

// lockFilePresent 检查锁文件是否存在
// 锁文件位于系统临时目录下的 .lock 文件
// 返回 true 表示锁文件存在，服务已就绪
func lockFilePresent() bool {
	// 构建锁文件路径
	path := filepath.Join(os.TempDir(), ".lock")
	// 检查文件是否存在
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}

	return true
}

// makeHealthHandler 创建健康检查处理器
// 健康检查端点 /_/health 用于判断服务是否正常运行
// 返回值：
//   - 200 OK: 服务正常接受连接
//   - 503 Service Unavailable: 服务不可用
//   - 405 Method Not Allowed: 非 GET 请求
//
// 参数 lockPresent 是检查锁文件是否存在的函数
func makeHealthHandler(lockPresent func() bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// 检查服务是否接受连接且锁文件存在
			if atomic.LoadInt32(&acceptingConnections) == 0 || lockPresent() == false {
				// 服务不可用
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			// 服务正常
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))

		default:
			// 不支持的 HTTP 方法
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// makeJWTAuthHandler 创建 JWT 认证中间件处理器
// 该处理器会验证请求中的 JWT Token，确保请求来自授权用户
// 参数：
//   - cfg: Watchdog 配置
//   - next: 认证通过后调用的下一个处理器
//
// 返回包装了 JWT 认证的处理器
func makeJWTAuthHandler(cfg config.WatchdogConfig, next http.Handler) (http.Handler, error) {
	// 获取函数命名空间
	namespace, err := getFnNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get function namespace: %w", err)
	}
	// 获取函数名称
	name, err := getFnName()
	if err != nil {
		return nil, fmt.Errorf("failed to get function name: %w", err)
	}

	// 构建 JWT 认证选项
	authOpts := auth.JWTAuthOptions{
		Name:           name,
		Namespace:      namespace,
		LocalAuthority: cfg.JWTAuthLocal,
		Debug:          cfg.JWTAuthDebug,
	}

	// 创建并返回 JWT 认证中间件
	return auth.NewJWTAuthMiddleware(authOpts, next)
}

// WriterCounter 是一个带字节计数功能的 io.Writer 包装器
// 用于统计写入的字节数，常用于记录响应大小
type WriterCounter struct {
	// w 是被包装的底层 io.Writer
	w io.Writer
	// bytes 记录已写入的总字节数
	bytes int64
}

// setWriter 设置底层 io.Writer
// 参数 w 是要包装的 io.Writer
func (nc *WriterCounter) setWriter(w io.Writer) {
	nc.w = w
}

// Bytes 返回已写入的总字节数
func (nc *WriterCounter) Bytes() int64 {
	return nc.bytes
}

// Write 实现 io.Writer 接口
// 将数据写入底层 Writer 并累计字节数
// 参数 p 是要写入的字节切片
// 返回写入的字节数和可能的错误
func (nc *WriterCounter) Write(p []byte) (int, error) {
	// 写入底层 Writer
	n, err := nc.w.Write(p)
	if err != nil {
		return n, err
	}

	// 累计已写入的字节数
	nc.bytes += int64(n)
	return n, err
}

// getFnName 从环境变量获取函数名称
// 环境变量名：OPENFAAS_NAME
// 返回函数名称或错误（如果环境变量未设置）
func getFnName() (string, error) {
	name, ok := os.LookupEnv("OPENFAAS_NAME")
	if !ok || len(name) == 0 {
		return "", fmt.Errorf("env variable 'OPENFAAS_NAME' not set")
	}

	return name, nil
}

// getFnNamespace 获取函数所在的命名空间
// 优先从环境变量 OPENFAAS_NAMESPACE 获取
// 如果环境变量未设置，则从 Kubernetes 服务账号文件读取
// 返回命名空间名称或错误
func getFnNamespace() (string, error) {
	// 首先尝试从环境变量获取
	if namespace, ok := os.LookupEnv("OPENFAAS_NAMESPACE"); ok {
		return namespace, nil
	}

	// 从 Kubernetes 服务账号文件读取命名空间
	nsVal, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}
	return string(nsVal), nil
}
