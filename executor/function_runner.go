// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package executor 提供函数执行器的接口定义
// 定义函数运行的通用规范与请求结构体
package executor

import (
	"io"
)

// FunctionRunner 函数运行器接口
// 所有函数执行器都必须实现该接口，用于统一执行函数调用
type FunctionRunner interface {
	// Run 执行函数调用
	// 参数 f 为函数执行的完整请求信息
	Run(f FunctionRequest) error
}

// FunctionRequest 函数执行请求结构体
// 存储一次函数调用所需的全部请求信息、参数、环境变量与IO流
type FunctionRequest struct {
	// RequestURI 请求的URI路径
	RequestURI string
	// Method HTTP请求方法
	Method string
	// UserAgent 请求客户端标识
	UserAgent string

	// Process 要执行的进程名称/路径
	Process string
	// ProcessArgs 执行进程的参数
	ProcessArgs []string
	// Environment 进程运行环境变量
	Environment []string

	// InputReader 函数输入读取流
	InputReader io.ReadCloser
	// OutputWriter 函数输出写入流
	OutputWriter io.Writer
	// ContentLength 请求体长度
	ContentLength *int64
}
