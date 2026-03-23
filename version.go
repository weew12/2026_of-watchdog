// Copyright (c) OpenFaaS Author(s) 2021. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// version.go 版本信息
package main

var (
	// Version watchdog 释放版本号
	Version string
	// GitCommit 最近一次 git 提交的 SHA 值
	// 用于构建时的版本信息
	GitCommit string
	// DevVersion 开发版本号
	DevVersion = "weew12 dev"
)

// BuildVersion 返回当前版本的 watchdog 如果没有版本号则返回开发版本“dev”
func BuildVersion() string {
	if len(Version) == 0 {
		return DevVersion
	}
	return Version
}
