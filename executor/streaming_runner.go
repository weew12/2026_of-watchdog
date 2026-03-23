// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package executor 提供函数执行器实现 ModeStreaming
// 支持流式函数调用，实时输出结果
package executor

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// StreamingFunctionRunner 流式函数运行器
// 每次函数调用都会创建一个独立的子进程，实时输出结果
type StreamingFunctionRunner struct {
	// ExecTimeout 函数执行超时时间
	ExecTimeout time.Duration
	// LogPrefix 是否为日志添加前缀
	LogPrefix bool
	// LogBufferSize 日志缓冲区大小
	LogBufferSize int
}

// Run 每次调用都创建子进程执行函数
// 支持标准输入输出、超时控制和日志输出
func (f *StreamingFunctionRunner) Run(req FunctionRequest) error {

	var cmd *exec.Cmd
	ctx := context.Background()
	if f.ExecTimeout.Nanoseconds() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.ExecTimeout)
		defer cancel()
	}

	cmd = exec.CommandContext(ctx, req.Process, req.ProcessArgs...)
	if req.InputReader != nil {
		defer req.InputReader.Close()
		cmd.Stdin = req.InputReader
	}

	cmd.Env = req.Environment
	cmd.Stdout = req.OutputWriter

	errPipe, _ := cmd.StderrPipe()

	// 将标准错误输出重定向并打印
	bindLoggingPipe("stderr", errPipe, os.Stderr, f.LogPrefix, f.LogBufferSize)

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Wait()
}
