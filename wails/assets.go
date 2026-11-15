package main

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Go embed 不接受中文文件名，所以 assets 被打包成 store-only 的 assets.zip；
// 这里用内存 zip 读取器把它伪装成 fs.FS，供 Wails 静态服务器直接读取（支持 Range）。
//
// 关键点：条目是 store（不压缩）的，每个文件的数据就是 assets.zip 里连续的一段。
// Open 直接返回指向这段字节的 SectionReader，不复制、不分配——旧实现每次请求都
// io.ReadAll 整个文件，视频 Range 请求一密集就把堆内存推得很高。
type zipFS struct {
	frontend fs.FS
	raw      io.ReaderAt // assets.zip 的只读视图（embed 数据本身，不复制到堆）
	size     int64
	files    map[string]*zip.File
}

var petAssets *zipFS

func newZipFS(frontend fs.FS) (*zipFS, error) {
	// 注意：这里不能 Close。这个文件就是后面所有读取的 ReaderAt 来源，
	// 关闭后素材会全部读不到（embed.FS 的 Close 恰好是空操作，才没在旧实现里暴露）。
	f, err := frontend.Open("assets.zip")
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	// embed.FS 的文件本身支持 ReadAt，可以直接当 zip 的底层 ReaderAt 用；
	// 万一换成不支持 ReadAt 的 fs 实现，退回整块读入（仍比逐次读整个文件省）。
	raw, ok := f.(io.ReaderAt)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return nil, err
		}
		raw = bytes.NewReader(b)
		size = int64(len(b))
	}
	zr, err := zip.NewReader(raw, size)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		m[zf.Name] = zf
	}
	return &zipFS{frontend: frontend, raw: raw, size: size, files: m}, nil
}

func (z *zipFS) Open(name string) (fs.File, error) {
	clean := path.Clean(strings.TrimPrefix(name, "/"))
	if strings.HasPrefix(clean, "assets/") {
		zf, ok := z.files[clean]
		if !ok {
			return nil, fs.ErrNotExist
		}
		if zf.Method == zip.Store {
			if off, err := zf.DataOffset(); err == nil {
				size := int64(zf.UncompressedSize64)
				if off >= 0 && size >= 0 && off+size <= z.size {
					return &memFile{
						r:    io.NewSectionReader(z.raw, off, size),
						name: path.Base(clean),
						size: size,
					}, nil
				}
			}
		}
		// 压缩条目（当前流水线不会产生）兜底：解压到内存
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		return &memFile{reader: bytes.NewReader(b), name: path.Base(clean), size: int64(len(b))}, nil
	}
	return z.frontend.Open(clean)
}

func (z *zipFS) read(name string) ([]byte, error) {
	zf, ok := z.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func readAsset(name string) ([]byte, error) {
	if petAssets == nil {
		return nil, fs.ErrNotExist
	}
	return petAssets.read(name)
}

func (z *zipFS) stats() map[string]interface{} {
	return map[string]interface{}{
		"files":    len(z.files),
		"zipBytes": z.size,
		"zeroCopy": true,
	}
}

// memFile 把 SectionReader 适配成 fs.File；Read/Seek 供 http 的 Range 处理使用。
type memFile struct {
	r      io.ReadSeeker
	reader *bytes.Reader // 仅压缩条目兜底时使用
	name   string
	size   int64
}

func (f *memFile) source() io.ReadSeeker {
	if f.r != nil {
		return f.r
	}
	return f.reader
}

func (f *memFile) Stat() (fs.FileInfo, error) { return memInfo{f}, nil }
func (f *memFile) Read(p []byte) (int, error) { return f.source().Read(p) }
func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	return f.source().Seek(offset, whence)
}
func (f *memFile) Close() error { return nil }

type memInfo struct{ f *memFile }

func (i memInfo) Name() string       { return i.f.name }
func (i memInfo) Size() int64        { return i.f.size }
func (i memInfo) Mode() fs.FileMode  { return 0644 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() interface{}   { return nil }
