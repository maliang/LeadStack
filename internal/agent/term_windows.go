//go:build windows

package agent

// startTerminalProcess 在 Windows 上不支持交互式终端（构建约束保证可编译）。
func startTerminalProcess(shell string, cols, rows int) (terminalProcess, error) {
	return nil, errTerminalUnsupported
}
