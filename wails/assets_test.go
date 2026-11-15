package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// 前端资源是从 store-only 的 assets.zip 里零拷贝切出来的，
// 这里用同样方式造一个包，确保切片偏移、Range 用的 Seek 都正确。
func TestZipFSServesStoreOnlyAssets(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("dafeiyu"), 4096)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"assets/anims/index.js", "assets/logo.png"} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets.zip"), buf.Bytes(), 0644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	z, err := newZipFS(os.DirFS(dir))
	if err != nil {
		t.Fatalf("newZipFS: %v", err)
	}
	// zipFS 会一直持有底层 ReaderAt，测试结束要自己关掉，否则 Windows 删不掉临时目录
	if c, ok := z.raw.(io.Closer); ok {
		defer c.Close()
	}
	if len(z.files) != 2 {
		t.Fatalf("want 2 entries, got %d", len(z.files))
	}

	// 完整读取：内容必须与写入时一致
	f, err := z.Open("/assets/logo.png")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch: want %d bytes, got %d", len(payload), len(got))
	}

	// Range 请求依赖 Seek：从中间读一段，长度与内容都要对
	sf, err := z.Open("assets/anims/index.js")
	if err != nil {
		t.Fatalf("open for seek: %v", err)
	}
	seeker, ok := sf.(io.Seeker)
	if !ok {
		t.Fatal("memFile must implement io.Seeker for HTTP Range")
	}
	const skip = 100
	if _, err := seeker.Seek(skip, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	tail, err := io.ReadAll(sf)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(tail) != len(payload)-skip {
		t.Fatalf("range read length: want %d, got %d", len(payload)-skip, len(tail))
	}
	if !bytes.Equal(tail, payload[skip:]) {
		t.Fatal("range read content mismatch")
	}

	// 不存在的资源要返回 ErrNotExist，别把整窗资源都读出来
	if _, err := z.Open("assets/nope.png"); err == nil {
		t.Fatal("missing asset should fail")
	}
}

// Windows 10（< 22621）才需要做毛玻璃修复；Win11 上必须什么都不做，
// 否则可能把 DWM 的 backdrop=None 覆盖掉。
func TestBackdropThresholdCoversWindows10(t *testing.T) {
	if backdropBuildThreshold != 22621 {
		t.Fatalf("backdrop threshold changed: %d", backdropBuildThreshold)
	}
	if windowsBuildNumber() == 0 {
		t.Fatal("RtlGetVersion should report a build number")
	}
}
