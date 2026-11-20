package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeFontName(t *testing.T) {
	cases := map[string]string{
		"我的 字体++": "我的-字体",
		"../evil": "evil",
		"a/b\\c":  "abc",
		"  ...  ": "",
		"FZSong":  "FZSong",
	}
	for in, want := range cases {
		if got := safeFontName(in); got != want {
			t.Errorf("safeFontName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFontMagic(t *testing.T) {
	for _, ok := range [][]byte{
		[]byte("wOF2xxxx"), []byte("wOFFxxxx"), []byte("OTTOxxxx"), []byte{0, 1, 0, 0},
	} {
		if !fontMagicOK(ok) {
			t.Errorf("fontMagicOK(%q) = false", ok[:4])
		}
	}
	for _, bad := range [][]byte{[]byte("MZ\x90\x00"), []byte("RIFF"), []byte("abc")} {
		if fontMagicOK(bad) {
			t.Errorf("fontMagicOK(%q) = true", bad)
		}
	}
}

func TestImportFontAndServe(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	a := &App{}

	src := filepath.Join(t.TempDir(), "我的 字体++.woff2")
	if err := os.WriteFile(src, []byte("wOF2-dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, err := a.importFontFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if name != "我的-字体.woff2" {
		t.Fatalf("name = %q", name)
	}
	if !a.validFontFamily("custom:" + name) {
		t.Fatal("imported font should be valid")
	}
	if a.validFontFamily("custom:../"+name) || a.validFontFamily("custom:missing.woff2") || a.validFontFamily("bogus") {
		t.Fatal("invalid font families must be rejected")
	}

	// /userfonts/ 由 charactersFS 映射到配置目录 fonts/
	served := &charactersFS{primary: os.DirFS(t.TempDir()), overlay: os.DirFS(a.userConfigDir())}
	f, err := served.Open("userfonts/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "wOF2" {
		t.Fatalf("served head = %q, err = %v", head, err)
	}
	if _, err := served.Open("userfonts/../dafeiyu-state.json"); err == nil {
		t.Fatal("path traversal must not be served")
	}
}

func TestImportFontRejectsBadFile(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	a := &App{}
	src := filepath.Join(t.TempDir(), "fake.ttf")
	if err := os.WriteFile(src, []byte("MZ not a font"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.importFontFile(src); err == nil {
		t.Fatal("non-font file must be rejected")
	}
	exe := filepath.Join(t.TempDir(), "font.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.importFontFile(exe); err == nil {
		t.Fatal("unsupported extension must be rejected")
	}
}
