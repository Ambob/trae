package main

import (
    "io"
    "net/http"
    "os"
    "path/filepath"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/dialog"
)

// ensureCJKFont checks for available CJK fonts; if none, prompts to download Noto Sans SC.
// It applies the theme upon success.
func ensureCJKFont(a fyne.App, w fyne.Window) {
    // Try environment or system fonts first (handled in useCJKTheme)
    // If a font exists, use it; else prompt the user.
    if applied := tryApplyAnyCJK(a); applied {
        return
    }

    dialog.NewConfirm("中文字体未检测到",
        "是否下载并使用 Noto Sans SC 字体? (约 15MB)",
        func(ok bool) {
            if !ok {
                return
            }
            // Start download
            pr := dialog.NewProgress("下载字体", "正在下载 Noto Sans SC...", w)
            pr.Show()
            go func() {
                defer pr.Hide()
                // Official GitHub raw URL (OTF). If you prefer TTF, replace with a TTF URL.
                url := "https://raw.githubusercontent.com/googlefonts/noto-cjk/main/Sans/OTF/SimplifiedChinese/NotoSansSC-Regular.otf"
                destDir := filepath.Join(".", "assets")
                destPath := filepath.Join(destDir, "NotoSansSC-Regular.otf")
                _ = os.MkdirAll(destDir, 0o755)
                if err := downloadFile(url, destPath); err != nil {
                    dialog.NewError(err, w).Show()
                    return
                }
                // Apply theme with the new font
                if b, err := os.ReadFile(destPath); err == nil {
                    a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(destPath), b)))
                }
            }()
        }, w).Show()
}

func tryApplyAnyCJK(a fyne.App) bool {
    // Use env-var or system or bundled asset if available
    // This reuses the logic from useCJKTheme but returns whether we applied a font.
    if p := os.Getenv("CJK_FONT_PATH"); p != "" {
        if isSupportedFontExt(p) {
            if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
                a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
                return true
            }
        }
    }
    if p := findSystemCJKFontPath(); p != "" {
        if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
            a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
            return true
        }
    }
    // Bundled asset: scan for any .ttf/.otf in assets
    if p := findAssetCJKFontPath(); p != "" {
        if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
            a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
            return true
        }
    }
    return false
}

func downloadFile(url, dest string) error {
    resp, err := http.Get(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return &downloadError{status: resp.Status}
    }
    f, err := os.Create(dest)
    if err != nil {
        return err
    }
    defer f.Close()
    _, err = io.Copy(f, resp.Body)
    return err
}

type downloadError struct{ status string }

func (e *downloadError) Error() string { return "下载失败: " + e.status }