//go:build !windows

package agent

// rootPath 返回根分区挂载点。
func rootPath() string {
	return "/"
}
