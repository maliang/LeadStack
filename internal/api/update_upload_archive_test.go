package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractUploadedBinaryArchives(t *testing.T) {
	dir := t.TempDir()
	content := []byte("fake-leadstack-binary")

	tarPath := filepath.Join(dir, "leadstack.tar.gz")
	tf, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(tf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "release/leadstack", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = tf.Close()
	out, err := extractUploadedBinary(tarPath)
	if err != nil {
		t.Fatalf("解压 tar.gz 失败: %v", err)
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, content) {
		t.Fatalf("tar.gz 内容不符: %q", got)
	}
	if _, err := os.Stat(tarPath); !os.IsNotExist(err) {
		t.Fatal("原始 tar.gz 应删除")
	}

	zipPath := filepath.Join(dir, "leadstack.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("leadstack")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(content)
	_ = zw.Close()
	_ = zf.Close()
	out, err = extractUploadedBinary(zipPath)
	if err != nil {
		t.Fatalf("解压 zip 失败: %v", err)
	}
	got, _ = os.ReadFile(out)
	if !bytes.Equal(got, content) {
		t.Fatalf("zip 内容不符: %q", got)
	}
}
