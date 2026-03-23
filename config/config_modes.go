// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// config_modes.go 配置模式 定义了 watchdog 的运行模式
package config

const (
	// ModeStreaming 流式模式
	// 实时将进程输出推送给调用方，无需等待进程执行完毕
	// 适用场景：监控、日志、长连接响应
	ModeStreaming = 1

	// ModeSerializing 序列化模式
	// 等待进程全部执行完成，缓冲所有响应后一次性返回
	// 适用场景：需要完整结果、不支持流式接收的调用方
	ModeSerializing = 2

	// Deprecated:
	// ModeAfterBurn 高性能模式
	// 针对性能优化的运行模式，减少额外开销提升执行速度
	ModeAfterBurn = 3

	// ModeHTTP HTTP代理模式
	// 将HTTP请求转发到后端HTTP服务处理
	// 适用场景：对接标准HTTP接口服务
	ModeHTTP = 4

	// ModeStatic 静态文件模式
	// 直接返回静态资源，不执行函数进程
	// 适用场景：HTML、CSS、JS、图片等静态资源服务
	ModeStatic = 5

	// ModeInproc 进程内执行模式
	// 在当前进程内直接执行函数逻辑，仅支持Go语言
	// 性能最高，无进程启动开销
	ModeInproc = 6
)

// WatchdogModeConst 将模式字符串转换为对应常量值
func WatchdogModeConst(mode string) int {
	switch mode {
	case "streaming":
		return ModeStreaming
	case "afterburn":
		return ModeAfterBurn
	case "serializing":
		return ModeSerializing
	case "http":
		return ModeHTTP
	case "static":
		return ModeStatic
	case "inproc":
		return ModeInproc
	default:
		return 0
	}
}

// WatchdogMode 将模式常量转换为对应字符串
func WatchdogMode(mode int) string {
	switch mode {
	case ModeStreaming:
		return "streaming"
	case ModeAfterBurn:
		return "afterburn"
	case ModeSerializing:
		return "serializing"
	case ModeHTTP:
		return "http"
	case ModeStatic:
		return "static"
	case ModeInproc:
		return "inproc"
	default:
		return "unknown"
	}
}
