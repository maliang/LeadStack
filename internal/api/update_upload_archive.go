package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractUploadedBinary 允许管理员上传发布归档（.tar.gz/.tgz/.zip）或裸二进制。
// 归档里的 leadstack 会被解到同目录临时文件；原始归档随后删除。
func extractUploadedBinary(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开上传文件失败: %w", err)
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(file, head)
	_ = file.Close()
	head = head[:n]
	switch {
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return extractTarGzBinary(path)
	case len(head) >= 2 && head[0] == 'P' && head[1] == 'K':
		return extractZipBinary(path)
	default:
		return path, nil
	}
}

func extractTarGzBinary(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("打开 tar.gz 失败: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取 tar.gz 失败: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !isControlBinaryName(header.Name) {
			continue
		}
		out := archivePath + ".bin"
		if err := writeExtractedBinary(out, tr); err != nil {
			return "", err
		}
		_ = os.Remove(archivePath)
		return out, nil
	}
	return "", errors.New("压缩包里没有找到 leadstack 可执行文件")
}

func extractZipBinary(archivePath string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开 zip 失败: %w", err)
	}
	defer func() { _ = zr.Close() }()
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !isControlBinaryName(entry.Name) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return "", fmt.Errorf("读取 zip 条目失败: %w", err)
		}
		out := archivePath + ".bin"
		err = writeExtractedBinary(out, rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		_ = os.Remove(archivePath)
		return out, nil
	}
	return "", errors.New("压缩包里没有找到 leadstack 可执行文件")
}

func isControlBinaryName(name string) bool {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	return base == "leadstack" || base == "leadstack.exe"
}

func writeExtractedBinary(path string, r io.Reader) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建解压文件失败: %w", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(r, upgradeUploadMaxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("解压 leadstack 失败: %w", copyErr)
	}
	if written > upgradeUploadMaxBytes {
		_ = os.Remove(path)
		return fmt.Errorf("解压后的 leadstack 超过 %d 字节上限", upgradeUploadMaxBytes)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("写入解压文件失败: %w", closeErr)
	}
	return nil
}
