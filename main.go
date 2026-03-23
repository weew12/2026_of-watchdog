// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package main 程序入口包
// 实现of-watchdog的启动、配置加载、健康检查、运行控制的入口逻辑
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/openfaas/of-watchdog/config"
	"github.com/openfaas/of-watchdog/pkg"
)

func main() {
	// 是否运行健康检查模式
	var runHealthcheck bool
	// 是否打印版本信息
	var versionFlag bool

	// 解析命令行参数：版本信息
	flag.BoolVar(&versionFlag, "version", false, "Print the version and exit")
	// 解析命令行参数：健康检查
	flag.BoolVar(&runHealthcheck,
		"run-healthcheck",
		false,
		"Check for the a lock-file, when using an exec healthcheck. Exit 0 for present, non-zero when not found.")

	// 解析命令行参数
	flag.Parse()

	// 打印版本信息
	printVersion()

	// 如果仅查看版本，直接退出
	if versionFlag {
		return
	}

	// 从环境变量加载Watchdog配置
	watchdogConfig, err := config.New(os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %s", err.Error())
		os.Exit(1)
	}

	// 创建Watchdog实例
	w := pkg.NewWatchdog(watchdogConfig)

	// 如果是健康检查模式，检查锁文件并退出
	if runHealthcheck {
		if w.LockFilePresent() {
			os.Exit(0)
		}

		fmt.Fprintf(os.Stderr, "unable to find lock file.\n")
		os.Exit(1)
	}

	// 创建上下文，用于优雅退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动Watchdog主服务
	if err := w.Start(ctx); err != nil {
		log.Printf("Error: %s\n", err.Error())
		os.Exit(1)
	}
}

// printVersion 打印 watchdog 版本信息与Git提交哈希
func printVersion() {
	sha := "unknown"
	if len(GitCommit) > 0 {
		sha = GitCommit
	}

	log.Printf("\033[32m[weew12] Version: %v\tSHA: %v\n\033[0m", BuildVersion(), sha)
}
