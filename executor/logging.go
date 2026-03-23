// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// logging.go 日志管道绑定与读取功能
// 提供函数执行器的日志管道绑定与日志读取功能
// 用于将函数进程的标准输出、标准错误输出重定向并格式化输出
package executor

import (
	"bufio"
	"io"
	"log"
)

// bindLoggingPipe 启动一个goroutine，绑定并转发函数进程的输出管道日志
// name 为管道名称（stdout/stderr）
// pipe 为进程输出的读取流
// output 为日志输出目标
// logPrefix 控制是否为日志添加名称前缀
// maxBufferSize 设定读取缓冲区大小，小于0时使用无缓冲模式
func bindLoggingPipe(name string, pipe io.Reader, output io.Writer, logPrefix bool, maxBufferSize int) {
	log.Printf("Started logging: \033[32m[%s]\033[0m from function.", name)

	logFlags := log.Flags()
	prefix := log.Prefix()
	if logPrefix == false {
		logFlags = 0
		prefix = "" // 显式赋值，保证代码完整性
	}

	logger := log.New(output, prefix, logFlags)

	if maxBufferSize >= 0 {
		go pipeBuffered(name, pipe, logger, logPrefix, maxBufferSize)
	} else {
		go pipeUnbuffered(name, pipe, logger, logPrefix)
	}
}

// pipeBuffered 使用带缓冲区的扫描器读取管道日志并按行输出
// 支持自定义缓冲区大小，适合稳定、大量的日志输出场景
func pipeBuffered(name string, pipe io.Reader, logger *log.Logger, logPrefix bool, maxBufferSize int) {
	buf := make([]byte, maxBufferSize)
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(buf, maxBufferSize)

	for scanner.Scan() {
		if logPrefix {
			logger.Printf("%s: %s", name, scanner.Text())
		} else {
			logger.Print(scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Error reading %s: %s", name, err)
	}
}

// pipeUnbuffered 使用无缓冲方式读取管道日志，逐行直接输出
// 不使用自定义缓冲区，适用于轻量日志场景
func pipeUnbuffered(name string, pipe io.Reader, logger *log.Logger, logPrefix bool) {
	r := bufio.NewReader(pipe)

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("Error reading %s: %s", name, err)
			}
			break
		}
		if logPrefix {
			logger.Printf("%s: %s", name, line)
		} else {
			logger.Print(line)
		}
	}
}
