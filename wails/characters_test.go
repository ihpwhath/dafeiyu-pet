package main

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPackForVariant(t *testing.T) {
	p := CatalogPack{
		ID: "gemini", URL: "full.zip", ZipBytes: 100,
		LiteURL: "lite.zip", LiteZipBytes: 40,
	}
	if got := packForVariant(p, false); got.URL != "full.zip" || got.ZipBytes != 100 {
		t.Fatalf("full variant changed: %+v", got)
	}
	if got := packForVariant(p, true); got.URL != "lite.zip" || got.ZipBytes != 40 {
		t.Fatalf("lite variant not selected: %+v", got)
	}
	legacy := CatalogPack{ID: "old", URL: "full.zip", ZipBytes: 100}
	if got := packForVariant(legacy, true); got.URL != "full.zip" || got.ZipBytes != 100 {
		t.Fatalf("legacy pack should fall back to full: %+v", got)
	}
}

func TestValidChar(t *testing.T) {
	for _, s := range []string{"v1", "gpt", "pack:claude", "pack:my_pet-1", "pack:gpt"} {
		if !validChar(s) {
			t.Fatalf("%s should be valid", s)
		}
	}
	for _, s := range []string{"", "whale1", "whale2", "v2", "v3", "v4", "v5", "v6", "gpt2", "pack:", "pack:MyPet", "pack:../x"} {
		if validChar(s) {
			t.Fatalf("%s should be invalid", s)
		}
	}
}

func TestFetchCatalogSelectsLite(t *testing.T) {
	const body = `{"packs":[{"id":"gemini","url":"full.zip","zipBytes":100,"liteUrl":"lite.zip","liteZipBytes":40}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	packs, err := fetchCharacterCatalog(srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].URL != "lite.zip" || packs[0].ZipBytes != 40 {
		t.Fatalf("lite catalog not applied: %+v", packs)
	}
	packs, err = fetchCharacterCatalog(srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	if packs[0].URL != "full.zip" || packs[0].ZipBytes != 100 {
		t.Fatalf("full catalog changed: %+v", packs[0])
	}
}

func testManifest(id string) string {
	return fmt.Sprintf(`{
		"schemaVersion": 1,
		"id": %q,
		"name": "测试娘",
		"cover": "cover.webp",
		"license": "CC0",
		"slots": {"idle": ["待机"], "click": "挥手", "sleep": ["睡觉"]},
		"actions": [
			{"id": "待机", "category": "待机", "file": "animations/idle.webm"},
			{"id": "挥手", "category": "点击回应", "file": "animations/wave.webm"},
			{"id": "睡觉", "category": "日常起居", "file": "animations/sleep.webm"}
		]
	}`, id)
}

func writeZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func validPackFiles(prefix, manifest string) map[string]string {
	files := map[string]string{
		prefix + "manifest.json":         manifest,
		prefix + "cover.webp":            "cover-bytes",
		prefix + "animations/idle.webm":  "video-idle",
		prefix + "animations/wave.webm":  "video-wave",
		prefix + "animations/sleep.webm": "video-sleep",
	}
	return files
}

func TestValidateManifest(t *testing.T) {
	good := func() *CharacterManifest {
		return &CharacterManifest{
			SchemaVersion: 1, ID: "my_pet-1", Name: "测试娘", Cover: "cover.webp",
			Slots: map[string]StringList{"idle": {"待机"}},
			Actions: []CharacterAction{
				{ID: "待机", File: "animations/idle.webm"},
				{ID: "挥手", File: "animations/wave.webm"},
			},
		}
	}
	if err := validateManifest(good()); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*CharacterManifest)
	}{
		{"reserved id", func(m *CharacterManifest) { m.ID = "v1" }},
		{"bad id case", func(m *CharacterManifest) { m.ID = "MyPet" }},
		{"missing name", func(m *CharacterManifest) { m.Name = " " }},
		{"no cover", func(m *CharacterManifest) { m.Cover = "" }},
		{"cover escapes", func(m *CharacterManifest) { m.Cover = "../cover.webp" }},
		{"no actions", func(m *CharacterManifest) { m.Actions = nil }},
		{"duplicate action", func(m *CharacterManifest) { m.Actions[1].ID = "待机" }},
		{"bad file ext", func(m *CharacterManifest) { m.Actions[0].File = "a.exe" }},
		{"file escapes", func(m *CharacterManifest) { m.Actions[0].File = "../a.webm" }},
		{"unknown slot", func(m *CharacterManifest) { m.Slots = map[string]StringList{"dance": {"待机"}} }},
		{"slot missing action", func(m *CharacterManifest) { m.Slots = map[string]StringList{"idle": {"不存在"}} }},
	}
	for _, c := range cases {
		m := good()
		c.edit(m)
		if err := validateUserImport(m); err == nil {
			t.Fatalf("%s should be rejected", c.name)
		}
	}

	sprite := good()
	sprite.Kind = "sprite"
	sprite.Actions[0].File = "spritesheet.webp"
	if err := validateUserImport(sprite); err == nil {
		t.Fatal("custom sprite pack should be rejected")
	}
}

func TestInstallCharacterFromWrappedZip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	a := &App{}

	evil := validPackFiles("", testManifest("my_pet-1"))
	evil["../evil.txt"] = "must-not-escape"
	evilZip := filepath.Join(tmp, "evil.zip")
	writeZip(t, evilZip, evil)
	if id, err := a.InstallCharacterFromFile(evilZip); err == nil {
		t.Fatalf("zip with ../ entry should be rejected, got id %s", id)
	}

	wrapped := validPackFiles("测试娘-v1/", testManifest("my_pet-1"))
	zipPath := filepath.Join(tmp, "wrapped.zip")
	writeZip(t, zipPath, wrapped)
	id, err := a.InstallCharacterFromFile(zipPath)
	if err != nil {
		t.Fatalf("wrapped zip should install: %v", err)
	}
	if id != "my_pet-1" {
		t.Fatalf("unexpected id: %s", id)
	}
	for _, rel := range []string{"manifest.json", "cover.webp", "animations/idle.webm", "animations/sleep.webm"} {
		if _, err := os.Stat(filepath.Join(a.charactersDir(), id, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	list := a.ListInstalledCharacters()
	if len(list) != 1 || len(list[0].Slots["idle"]) != 1 || list[0].Slots["idle"][0] != "待机" {
		t.Fatalf("slots not round-tripped: %+v", list)
	}
}
