// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package executor 提供函数执行器实现 ModeSerializingFork
// 支持序列化分叉进程函数调用，缓冲全部输出后再返回结果
package executor

import (
	"context"
	"fmt"
	"io"
	"os"

	units "github.com/docker/go-units"

	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SerializingForkFunctionRunner 序列化分叉进程运行器
// 每次调用都会创建子进程，缓冲全部输出后再返回结果
type SerializingForkFunctionRunner struct {
	// ExecTimeout 函数执行超时时间
	ExecTimeout time.Duration
	// LogPrefix 是否为日志添加前缀
	LogPrefix bool
	// LogBufferSize 日志缓冲区大小
	LogBufferSize int
}

// Run 创建子进程执行函数，缓冲全部结果后统一返回
// 忽略kube-probe健康检查日志，记录请求耗时与响应大小
func (f *SerializingForkFunctionRunner) Run(req FunctionRequest, w http.ResponseWriter) error {
	start := time.Now()
	body, err := serializeFunction(req, f)
	if err != nil {
		w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", time.Since(start).Seconds()))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		done := time.Since(start)

		if !strings.HasPrefix(req.UserAgent, "kube-probe") {
			log.Printf("%s %s - %d - ContentLength: %s (%.4fs)", req.Method, req.RequestURI, http.StatusOK, units.HumanSize(float64(len(err.Error()))), done.Seconds())
		}

		return err
	}

	w.Header().Set("X-Duration-Seconds", fmt.Sprintf("%f", time.Since(start).Seconds()))
	w.WriteHeader(200)

	bodyLen := 0
	if body != nil {
		_, err = w.Write(*body)
		bodyLen = len(*body)
	}

	done := time.Since(start)

	if !strings.HasPrefix(req.UserAgent, "kube-probe") {
		log.Printf("%s %s - %d - ContentLength: %s (%.4fs)", req.Method, req.RequestURI, http.StatusOK, units.HumanSize(float64(bodyLen)), done.Seconds())
	}

	return err
}

// serializeFunction 执行进程并序列化读取全部输出
// 读取请求体、创建带超时的子进程、绑定错误日志
func serializeFunction(req FunctionRequest, f *SerializingForkFunctionRunner) (*[]byte, error) {

	if req.InputReader != nil {
		defer req.InputReader.Close()
	}

	var cmd *exec.Cmd
	ctx := context.Background()
	if f.ExecTimeout.Nanoseconds() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.ExecTimeout)
		defer cancel()
	}

	cmd = exec.CommandContext(ctx, req.Process, req.ProcessArgs...)
	cmd.Env = req.Environment

	var data []byte

	if req.InputReader != nil {
		reader := req.InputReader.(io.Reader)

		// 根据Content-Length限制读取长度
		if req.ContentLength != nil && *req.ContentLength > 0 {
			reader = io.LimitReader(req.InputReader, *req.ContentLength)
		}

		var err error
		data, err = io.ReadAll(reader)

		if err != nil {
			return nil, err
		}

	}

	stdout, _ := cmd.StdoutPipe()
	stdin, _ := cmd.StdinPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// 绑定标准错误日志输出
	bindLoggingPipe("stderr", stderr, os.Stderr, f.LogPrefix, f.LogBufferSize)

	// 与进程进行管道通信
	functionRes, errors := pipeToProcess(stdin, stdout, &data)
	if len(errors) > 0 {
		return nil, errors[0]
	}

	err := cmd.Wait()

	return functionRes, err
}

// pipeToProcess 并发读写进程管道
// 写入输入数据、读取输出结果，使用WaitGroup同步
func pipeToProcess(stdin io.WriteCloser, stdout io.Reader, data *[]byte) (*[]byte, []error) {
	var functionResult *[]byte
	var errors []error

	errChannel := make(chan error)

	// 收集管道操作错误
	go func() {
		for goErr := range errChannel {
			errors = append(errors, goErr)
		}
		close(errChannel)
	}()

	wg := sync.WaitGroup{}
	wg.Add(2)

	// 协程写入标准输入
	go func(c chan error) {
		_, err := stdin.Write(*data)
		stdin.Close()

		if err != nil {
			c <- err
		}

		wg.Done()
	}(errChannel)

	// 协程读取标准输出
	go func(c chan error) {
		var err error
		result, err := io.ReadAll(stdout)
		functionResult = &result
		if err != nil {
			c <- err
		}

		wg.Done()
	}(errChannel)

	wg.Wait()

	return functionResult, errors
}
