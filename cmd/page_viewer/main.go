package main

import (
    "context"
    "fmt"
    "net/http"
    "os"
    "time"

    webview "github.com/webview/webview_go"
)

func title(lang string) string {
    if lang == "zh" { return "设备网页" }
    return "Device Web Page"
}

func loadingText(lang string) string {
    if lang == "zh" { return "正在加载设备页面…" }
    return "Loading device page…"
}

func errorText(lang string) string {
    if lang == "zh" { return "无法打开设备页面，请检查设备网页服务是否在线" }
    return "Unable to open device page. Please check if the service is online."
}

func viewerHTML(url, lang string) string {
    return fmt.Sprintf(`<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    html, body { height: 100%%; }
    body { margin: 0; background: #f7f7f8; color: #333; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, "Noto Sans SC", "Microsoft YaHei", sans-serif; }
    #loader { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; gap: 12px; z-index: 10; }
    .spinner { border: 4px solid #eee; border-top: 4px solid #888; border-radius: 50%%; width: 36px; height: 36px; animation: spin 1s linear infinite; }
    @keyframes spin { 0%% { transform: rotate(0deg) } 100%% { transform: rotate(360deg) } }
    #error { position: absolute; inset: 0; display: none; align-items: center; justify-content: center; }
    .errorBox { background: #fff; border: 1px solid #ddd; border-radius: 8px; padding: 16px 20px; box-shadow: 0 2px 8px rgba(0,0,0,0.08); color: #b00020; }
  </style>
</head>
<body>
  <div id="loader"><div class="spinner"></div><div>%s</div></div>
  <div id="error"><div class="errorBox">%s</div></div>
  <script>
    window.app = {
      showError: function() {
        var loader = document.getElementById('loader');
        var err = document.getElementById('error');
        loader.style.display = 'none';
        err.style.display = 'flex';
      }
    };
  </script>
</body>
</html>`, title(lang), loadingText(lang), errorText(lang))
}

func main() {
    // Args: url, lang
    url := ""
    lang := "zh"
    if len(os.Args) > 1 { url = os.Args[1] }
    if len(os.Args) > 2 { lang = os.Args[2] }
    if url == "" {
        url = "http://127.0.0.1:8000"
    }

    wv := webview.New(false)
    if wv == nil {
        fmt.Fprintf(os.Stderr, "failed to create webview\n")
        os.Exit(1)
        return
    }
    defer wv.Destroy()
    wv.SetTitle(title(lang))
    // Fixed size 1280x800, non-resizable
    wv.SetSize(1280, 800, webview.HintFixed)
    // Show loading page immediately
    wv.SetHtml(viewerHTML(url, lang))

    // Pre-check availability and then navigate or show error
    go func() {
        // Use short timeout; if not reachable or non-2xx/3xx, show error
        ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
        defer cancel()
        req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
        client := &http.Client{ Timeout: 7 * time.Second }
        resp, err := client.Do(req)
        // small delay to ensure Run has started before Dispatch
        time.Sleep(150 * time.Millisecond)
        if err != nil {
            wv.Dispatch(func(){ wv.Eval("window.app.showError()") })
            return
        }
        resp.Body.Close()
        if resp.StatusCode >= 200 && resp.StatusCode < 400 {
            // Navigate replaces our loader HTML; page renders directly
            wv.Dispatch(func(){ wv.Navigate(url) })
        } else {
            wv.Dispatch(func(){ wv.Eval("window.app.showError()") })
        }
    }()

    wv.Run()
}