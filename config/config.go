// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// config.go 配置解析器 用于解析环境变量和命令行参数 生成 WatchdogConfig 结构体
// 用于配置 watchdog 服务的运行参数
package config

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WatchdogConfig watchdog服务的配置结构体
type WatchdogConfig struct {
	// TCPPort 监听的TCP端口
	TCPPort int
	// HTTPReadTimeout HTTP请求读取超时时间
	HTTPReadTimeout time.Duration
	// HTTPWriteTimeout HTTP响应写入超时时间
	HTTPWriteTimeout time.Duration
	// ExecTimeout 函数执行超时时间
	ExecTimeout time.Duration
	// HealthcheckInterval 健康检查间隔时间
	HealthcheckInterval time.Duration
	// FunctionProcess 需要执行的函数进程命令
	// 示例：/bin/echo "Hello World"
	FunctionProcess string
	// ContentType HTTP响应的内容类型
	// 示例：application/json
	ContentType string
	// InjectCGIHeaders 是否注入CGI标准请求头
	InjectCGIHeaders bool
	// OperationalMode watchdog运行模式
	// 可选值：streaming、afterburn、serializing、http、static、inproc
	OperationalMode int
	// SuppressLock 是否禁用执行锁
	// true=不使用锁，false=使用锁
	SuppressLock bool
	// UpstreamURL 上游服务地址
	// 示例：http://localhost:8080
	UpstreamURL string
	// StaticPath 静态文件服务路径
	// 示例：/static/
	StaticPath string

	// BufferHTTPBody 是否将HTTP请求体缓冲到内存
	// 用于兼容不支持分块编码的服务器
	BufferHTTPBody bool

	// MetricsPort Prometheus指标服务监听端口
	MetricsPort int

	// MaxInflight 最大并发处理请求数
	// 超出限制直接返回429状态码
	MaxInflight int

	// PrefixLogs 是否为函数日志添加时间戳和流标识
	// true=添加，false=不添加
	PrefixLogs bool

	// LogBufferSize 标准输出/错误日志的扫描缓冲区大小
	LogBufferSize int

	// ReadyEndpoint 自定义健康检查就绪路径
	// 非空时，/_/ready 端点会代理请求到此路径
	// 示例：/ready
	ReadyEndpoint string

	// JWTAuthentication 是否启用JWT认证
	// 使用OpenFaaS网关作为签发方
	JWTAuthentication bool

	// JWTAuthDebug 是否开启JWT认证调试日志
	JWTAuthDebug bool

	// JWTAuthLocal JWT认证是否使用本地网关
	// true=使用本地网关(127.0.0.1:8000)，false=使用集群内网关
	JWTAuthLocal bool

	// LogCallId 是否在HTTP模式日志中包含请求ID前缀
	LogCallId bool

	// Handler inproc模式下使用的HTTP处理函数
	Handler http.HandlerFunc
}

// Process 解析函数进程命令，返回可执行文件路径和参数列表
func (w WatchdogConfig) Process() (string, []string) {
	parts := strings.Split(w.FunctionProcess, " ")

	if len(parts) > 1 {
		return parts[0], parts[1:]
	}

	return parts[0], []string{}
}

// SetHandler 设置inproc模式的HTTP处理器
func (w *WatchdogConfig) SetHandler(handler http.HandlerFunc) {
	w.Handler = handler
}

// New 根据环境变量创建watchdog配置实例
func New(env []string) (WatchdogConfig, error) {
	defaultTimeout := time.Second * 30

	envMap := mapEnv(env)

	var (
		functionProcess string
		upstreamURL     string
	)

	logBufferSize := bufio.MaxScanTokenSize

	// 兼容旧版本默认开启日志前缀
	prefixLogs := true
	if val, exists := envMap["prefix_logs"]; exists {
		res, err := strconv.ParseBool(val)
		if err == nil {
			prefixLogs = res
		}
	}

	// 读取函数进程配置，支持两个环境变量
	if val, exists := envMap["fprocess"]; exists {
		functionProcess = val
	}
	if val, exists := envMap["function_process"]; exists {
		functionProcess = val
	}

	// 读取上游地址，支持两个环境变量
	if val, exists := envMap["upstream_url"]; exists {
		upstreamURL = val
	}
	if val, exists := envMap["http_upstream_url"]; exists {
		upstreamURL = val
	}

	// 不配置的话，默认使用 application/octet-stream 二进制内容类型
	contentType := "application/octet-stream"
	if val, exists := envMap["content_type"]; exists {
		contentType = val
	}

	// 不配置的话，默认使用 /home/app/public 作为静态文件服务路径
	staticPath := "/home/app/public"
	if val, exists := envMap["static_path"]; exists {
		staticPath = val
	}

	// 不配置的话，默认使用 30 秒作为 HTTP 响应写入超时时间
	writeTimeout := getDuration(envMap, "write_timeout", defaultTimeout)
	// 不配置的话，默认使用 30 秒作为健康检查间隔时间
	healthcheckInterval := writeTimeout
	if val, exists := envMap["healthcheck_interval"]; exists {
		healthcheckInterval = parseIntOrDurationValue(val, writeTimeout)
	}

	// 配置日志缓冲区大小，默认使用 bufio.MaxScanTokenSize 作为最大缓冲区大小
	if val, exists := envMap["log_buffer_size"]; exists {
		var err error
		if logBufferSize, err = strconv.Atoi(val); err != nil {
			return WatchdogConfig{}, fmt.Errorf("invalid log_buffer_size value: %s, error: %w", val, err)
		}
	}

	var logCallId bool
	// 不配置的话，默认不包含请求ID前缀
	if val, exists := envMap["log_callid"]; exists {
		if val == "1" {
			logCallId = true
		} else {
			logCallId, _ = strconv.ParseBool(val)
		}
	}

	// 初始化配置结构体
	c := WatchdogConfig{
		TCPPort:             getInt(envMap, "port", 8080),
		HTTPReadTimeout:     getDuration(envMap, "read_timeout", defaultTimeout),
		HTTPWriteTimeout:    writeTimeout,
		HealthcheckInterval: healthcheckInterval,
		FunctionProcess:     functionProcess,
		StaticPath:          staticPath,
		InjectCGIHeaders:    true,
		ExecTimeout:         getDuration(envMap, "exec_timeout", defaultTimeout),
		OperationalMode:     ModeStreaming,
		ContentType:         contentType,
		SuppressLock:        getBool(envMap, "suppress_lock"),
		UpstreamURL:         upstreamURL,
		BufferHTTPBody:      getBools(envMap, "buffer_http", "http_buffer_req_body"),
		MetricsPort:         8081,
		MaxInflight:         getInt(envMap, "max_inflight", 0),
		PrefixLogs:          prefixLogs,
		LogBufferSize:       logBufferSize,
		ReadyEndpoint:       envMap["ready_path"],
		LogCallId:           logCallId,
	}

	// 设置运行模式
	if val := envMap["mode"]; len(val) > 0 {
		c.OperationalMode = WatchdogModeConst(val)
	}

	// 校验HTTP写入超时
	if writeTimeout == 0 {
		return c, fmt.Errorf("HTTP write timeout must be over 0s")
	}

	// 非静态/内联模式必须配置函数进程
	if len(c.FunctionProcess) == 0 && c.OperationalMode != ModeStatic && c.OperationalMode != ModeInproc {
		return c, fmt.Errorf(`provide a "function_process" or "fprocess" environmental variable for your function`)
	}

	// JWT认证相关配置
	c.JWTAuthentication = getBool(envMap, "jwt_auth")
	c.JWTAuthDebug = getBool(envMap, "jwt_auth_debug")
	c.JWTAuthLocal = getBool(envMap, "jwt_auth_local")

	return c, nil
}

// mapEnv 将环境变量切片转换为键值对映射
// 格式错误的环境变量会打印日志
func mapEnv(env []string) map[string]string {
	mapped := map[string]string{}

	for _, val := range env {
		sep := strings.Index(val, "=")

		if sep > 0 {
			key := val[0:sep]
			value := val[sep+1:]
			mapped[key] = value
		} else {
			log.Printf("无效的环境变量格式: %s", val)
		}
	}

	return mapped
}

// getDuration 从环境变量获取时长配置
// 不存在则返回默认值，格式错误也返回默认值
func getDuration(env map[string]string, key string, defaultValue time.Duration) time.Duration {
	if val, exists := env[key]; exists {
		return parseIntOrDurationValue(val, defaultValue)
	}

	return defaultValue
}

// parseIntOrDurationValue 解析字符串为时长
// 纯数字按秒解析，支持标准时长格式（1s、1m等）
// 解析失败返回默认值
func parseIntOrDurationValue(val string, fallback time.Duration) time.Duration {
	if len(val) > 0 {
		parsedVal, parseErr := strconv.Atoi(val)
		if parseErr == nil && parsedVal >= 0 {
			return time.Duration(parsedVal) * time.Second
		}
	}

	duration, durationErr := time.ParseDuration(val)
	if durationErr != nil {
		return fallback
	}
	return duration
}

// getInt 从环境变量获取整数配置
// 不存在或格式错误返回默认值
func getInt(env map[string]string, key string, defaultValue int) int {
	result := defaultValue
	if val, exists := env[key]; exists {
		parsed, _ := strconv.Atoi(val)
		result = parsed

	}

	return result
}

// getBool 从环境变量获取布尔值
// 支持true/1判定为真
func getBool(env map[string]string, key string) bool {
	if env[key] == "true" || env[key] == "1" {
		return true
	}

	return false
}

// getBools 读取多个环境变量布尔值
// 任意一个为真则返回真
func getBools(env map[string]string, key ...string) bool {
	v := false
	for _, k := range key {
		if getBool(env, k) == true {
			v = true
			break
		}
	}
	return v
}
