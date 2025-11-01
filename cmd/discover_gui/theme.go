package main

import (
    "image/color"
    "os"
    "path/filepath"
    "runtime"
    "strings"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/theme"
)

// useCJKTheme tries to load a font that supports Chinese characters.
// Place a font file at ./assets/NotoSansSC-Regular.ttf (or change the path below).
// If not found, it keeps the default theme.
func useCJKTheme(a fyne.App) {
    // 1) Environment override
    if p := os.Getenv("CJK_FONT_PATH"); p != "" {
        if isSupportedFontExt(p) {
            if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
                a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
                return
            }
        }
    }

    // 2) System fonts (platform-specific locations)
    if p := findSystemCJKFontPath(); p != "" {
        if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
            a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
            return
        }
    }

    // 3) Bundled asset fallback: scan ./assets for any .ttf/.otf
    if p := findAssetCJKFontPath(); p != "" {
        if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
            a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
            return
        }
    }
    // If none found, keep default theme (English UI will avoid garbling)
}

func findSystemCJKFontPath() string {
    var candidates []string
    switch runtime.GOOS {
    case "darwin":
        candidates = []string{
            // Prefer TTF/OTF only (Fyne does not support TTC collections)
            "/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
            "/Library/Fonts/Arial Unicode.ttf",
            "/Library/Fonts/Microsoft YaHei.ttf",
        }
    case "windows":
        candidates = []string{
            `C:\\Windows\\Fonts\\msyh.ttf`,
            `C:\\Windows\\Fonts\\msyhl.ttf`,
            `C:\\Windows\\Fonts\\simhei.ttf`,
            `C:\\Windows\\Fonts\\SimSun.ttf`,
            `C:\\Windows\\Fonts\\Deng.ttf`, // DengXian
        }
    default: // linux and others
        candidates = []string{
            "/usr/share/fonts/truetype/noto/NotoSansSC-Regular.ttf",
            "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
            "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
            "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttf",
            "/usr/share/fonts/truetype/arphic/ukai.ttf", // AR PL UKai
        }
    }
    for _, p := range candidates {
        if !isSupportedFontExt(p) { // skip TTC etc.
            continue
        }
        if _, err := os.Stat(p); err == nil {
            return p
        }
    }
    return ""
}

func isSupportedFontExt(path string) bool {
    ext := strings.ToLower(filepath.Ext(path))
    return ext == ".ttf" || ext == ".otf"
}

func findAssetCJKFontPath() string {
    dir := filepath.Join(".", "assets")
    entries, err := os.ReadDir(dir)
    if err != nil {
        return ""
    }
    for _, e := range entries {
        if e.IsDir() {
            continue
        }
        name := e.Name()
        p := filepath.Join(dir, name)
        if isSupportedFontExt(p) {
            return p
        }
    }
    return ""
}

type cjkTheme struct {
    base fyne.Theme
    font fyne.Resource
}

func newCJKTheme(fontRes fyne.Resource) fyne.Theme {
    return &cjkTheme{base: theme.DefaultTheme(), font: fontRes}
}

func (t *cjkTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
    return t.base.Color(n, v)
}

func (t *cjkTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
    return t.base.Icon(n)
}

func (t *cjkTheme) Font(s fyne.TextStyle) fyne.Resource {
    // Use custom CJK-capable font for all styles (fallback to base if missing)
    if t.font != nil {
        return t.font
    }
    return t.base.Font(s)
}

func (t *cjkTheme) Size(n fyne.ThemeSizeName) float32 {
    return t.base.Size(n)
}