// Package buildinfo 由 -ldflags 注入版本信息。
package buildinfo

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
	// BuildTs 为 UTC 构建时间（yyyyMMddHHmmss），由 ldflags 注入，
	// 是版本更新检测用的可比键；"0" 表示未知（dev 构建）。
	BuildTs = "0"
)
