// Package rf4server exposes repository-level assets that are compiled into the binaries.
package rf4server

import _ "embed"

// README 是仓库根目录 README.md 的编译期内嵌副本。
//
//go:embed README.md
var README string
