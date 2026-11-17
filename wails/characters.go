package main

// 形象包（按需下载的角色）：默认只内置大肥鱼 v1，其余角色以 zip 形式下载到用户目录，
// 由 charactersFS 覆盖到内嵌资源之上——引擎侧不需要知道资源来自哪个包。

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CharacterAction 是 manifest 里的单条动作
type CharacterAction struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	File       string `json:"file"`
	Frames     int    `json:"frames"`
	FPS        int    `json:"fps"`
	DurationMs int    `json:"durationMs"`
}

// CharacterManifest 是形象包自带的元信息（包内 manifest.json）
type CharacterManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	SourcePackID  string            `json:"sourcePackId"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Cover         string            `json:"cover"`
	Source        string            `json:"source"`
	License       string            `json:"license"`
	Actions       []CharacterAction `json:"actions"`
}

// InstalledCharacter 是回给前端的精简结构
type InstalledCharacter struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Desc     string            `json:"description"`
	License  string            `json:"license"`
	Cover    string            `json:"cover"` // 相对 URL，前端可直接 img src
	Actions  []CharacterAction `json:"actions"`
	Bytes    int64             `json:"bytes"`
	Category []string          `json:"categories"` // 去重后的分类
}

func (a *App) userConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.Getenv("APPDATA")
	}
	return filepath.Join(dir, appDataDirName())
}

func (a *App) charactersDir() string {
	return filepath.Join(a.userConfigDir(), "characters")
}

// charactersFS 把用户目录下的形象包叠到内嵌资源之上：
// characters/<id>/xxx 优先读磁盘，读不到再回落到 assets.zip。
type charactersFS struct {
	primary fs.FS
	overlay fs.FS
}

func (c *charactersFS) Open(name string) (fs.File, error) {
	clean := strings.TrimPrefix(strings.ReplaceAll(name, "\\", "/"), "/")
	if strings.HasPrefix(clean, "characters/") && c.overlay != nil {
		if f, err := c.overlay.Open(clean); err == nil {
			return f, nil
		}
	}
	return c.primary.Open(name)
}

// loadInstalledCharacter 读取某个已安装包的 manifest
func (a *App) loadInstalledCharacter(id string) (*CharacterManifest, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return nil, errors.New("非法的形象 id")
	}
	p := filepath.Join(a.charactersDir(), id, "manifest.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m CharacterManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.ID == "" {
		m.ID = id
	}
	return &m, nil
}

// ListInstalledCharacters 供前端启动时拉取：已安装的形象包 + 动作表
func (a *App) ListInstalledCharacters() []InstalledCharacter {
	out := []InstalledCharacter{}
	entries, err := os.ReadDir(a.charactersDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := a.loadInstalledCharacter(e.Name())
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		cats := []string{}
		var size int64
		for _, act := range m.Actions {
			if act.Category != "" && !seen[act.Category] {
				seen[act.Category] = true
				cats = append(cats, act.Category)
			}
			if st, err := os.Stat(filepath.Join(a.charactersDir(), m.ID, act.File)); err == nil {
				size += st.Size()
			}
		}
		cover := ""
		if m.Cover != "" {
			cover = fmt.Sprintf("../characters/%s/%s", m.ID, m.Cover)
		}
		out = append(out, InstalledCharacter{
			ID: m.ID, Name: m.Name, Desc: m.Description, License: m.License,
			Cover: cover, Actions: m.Actions, Bytes: size, Category: cats,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// InstallCharacter 下载 zip 并解压到 characters/<id>/。
// url 由前端从远程索引里取，下载完成后原子替换，避免半成品被读到。
func (a *App) InstallCharacter(id, url string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return errors.New("非法的形象 id")
	}
	root := a.charactersDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmpZip := filepath.Join(root, id+".zip.part")
	defer os.Remove(tmpZip)

	cli := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cli.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载失败 HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(tmpZip)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	var written int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			written += int64(n)
			if total > 0 {
				a.emit("character-progress", map[string]interface{}{
					"id": id, "downloaded": written, "total": total,
				})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	f.Close()

	if err := a.extractPack(id, tmpZip); err != nil {
		return err
	}
	return nil
}

// InstallCharacterFromFile 从本地 zip 安装（形象商店的「导入」按钮走这条路）
func (a *App) InstallCharacterFromFile(zipPath string) (string, error) {
	id, err := packIDFromZip(zipPath)
	if err != nil {
		return "", err
	}
	if err := a.extractPack(id, zipPath); err != nil {
		return "", err
	}
	return id, nil
}

// packIDFromZip 读 zip 里的 manifest.json 拿到包 id
func packIDFromZip(zipPath string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) != "manifest.json" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		if err != nil {
			return "", err
		}
		var m CharacterManifest
		if err := json.Unmarshal(b, &m); err != nil {
			return "", err
		}
		if m.ID == "" {
			return "", errors.New("manifest 缺少 id")
		}
		return m.ID, nil
	}
	return "", errors.New("zip 内没有 manifest.json")
}

// extractPack 解压到 characters/<id>/，先写临时目录再原子改名，避免半成品被引擎读到
func (a *App) extractPack(id, zipPath string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return errors.New("非法的形象 id")
	}
	root := a.charactersDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	tmpDir := filepath.Join(root, id+".installing")

	os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("zip 损坏: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		name := filepath.Clean(zf.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return errors.New("zip 内含非法路径")
		}
		dst := filepath.Join(tmpDir, name)
		if zf.FileInfo().IsDir() {
			os.MkdirAll(dst, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if cerr != nil {
			return cerr
		}
	}
	if _, err := a.loadInstalledCharacterIn(tmpDir); err != nil {
		return fmt.Errorf("包内 manifest 无效: %w", err)
	}

	// 原子落位
	final := filepath.Join(root, id)
	os.RemoveAll(final + ".old")
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, final+".old"); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpDir, final); err != nil {
		return err
	}
	os.RemoveAll(final + ".old")
	return nil
}

func (a *App) loadInstalledCharacterIn(dir string) (*CharacterManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m CharacterManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// RemoveCharacter 卸载形象包
func (a *App) RemoveCharacter(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return errors.New("非法的形象 id")
	}
	return os.RemoveAll(filepath.Join(a.charactersDir(), id))
}

// ── 形象包目录（托管在 GitHub 的一个分支上，用加速链接下载）───────────────
// catalog 里的每个包都带 url 字段，已经拼好加速前缀，前端直接拿来下载。
const packCatalogPrimary = "https://ghfast.top/https://raw.githubusercontent.com/shenliuyu18/dafeiyu-pet/packs/characters.json"
const packCatalogFallback = "https://raw.githubusercontent.com/shenliuyu18/dafeiyu-pet/packs/characters.json"

type CatalogPack struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Desc        string `json:"description"`
	Actions     int    `json:"actions"`
	Zip         string `json:"zip"`
	ZipBytes    int64  `json:"zipBytes"`
	PlusFor     string `json:"plusFor"`
	Cover       string `json:"cover"`
	URL         string `json:"url"`
	InstallHint string `json:"installHint"`
}

// FetchCharacterCatalog 拉取可下载的形象包列表，失败返回空列表（商店里只显示已安装的）
func (a *App) FetchCharacterCatalog() ([]CatalogPack, error) {
	var lastErr error
	for _, u := range []string{packCatalogPrimary, packCatalogFallback} {
		cli := &http.Client{Timeout: 15 * time.Second}
		resp, err := cli.Get(u)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		var doc struct {
			Packs []CatalogPack `json:"packs"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			lastErr = err
			continue
		}
		return doc.Packs, nil
	}
	return nil, lastErr
}
