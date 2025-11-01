package main

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "path/filepath"
    "os"
    "os/exec"
    "strings"
    "time"
    "sync"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/app"
    "fyne.io/fyne/v2/container"
    "fyne.io/fyne/v2/dialog"
    "fyne.io/fyne/v2/storage"
    "fyne.io/fyne/v2/theme"
    "fyne.io/fyne/v2/widget"
    // webview is moved to a dedicated helper binary to avoid UI thread conflicts
)

// LoadingManager handles minimum display duration for loading states
type LoadingManager struct {
    mu sync.Mutex
    startTime time.Time
    isLoading bool
    minDuration time.Duration
    statusWidget *widget.Label
}

func NewLoadingManager() *LoadingManager {
    return &LoadingManager{
        minDuration: 500 * time.Millisecond, // 500ms minimum display
    }
}

func (lm *LoadingManager) SetStatusWidget(status *widget.Label) {
    lm.mu.Lock()
    defer lm.mu.Unlock()
    lm.statusWidget = status
}

func (lm *LoadingManager) StartLoading() {
    lm.mu.Lock()
    defer lm.mu.Unlock()
    lm.startTime = time.Now()
    lm.isLoading = true
}

func (lm *LoadingManager) FinishLoading(callback func()) {
    lm.mu.Lock()
    defer lm.mu.Unlock()
    
    if !lm.isLoading {
        callback()
        return
    }
    
    elapsed := time.Since(lm.startTime)
    remaining := lm.minDuration - elapsed
    
    lm.isLoading = false
    
    if remaining > 0 {
        // Wait for remaining time before executing callback
        go func() {
            time.Sleep(remaining)
            callback()
        }()
    } else {
        callback()
    }
}

// Enhanced status update with smooth transitions
func (lm *LoadingManager) UpdateStatus(text string) {
    if lm.statusWidget != nil {
        // Add a subtle animation effect by briefly changing the text style
        go func() {
            // Small delay for visual smoothness
            time.Sleep(50 * time.Millisecond)
            lm.statusWidget.SetText(text)
            lm.statusWidget.Refresh()
        }()
    }
}

// fixedSplitLayout lays out two children side-by-side with a fixed ratio (left:right).
type fixedSplitLayout struct{ ratio float32 }

func (l fixedSplitLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
    if len(objs) < 2 {
        return
    }
    if len(objs) == 2 {
        left := objs[0]
        right := objs[1]
        lw := size.Width * l.ratio
        if lw < 0 { lw = 0 }
        rw := size.Width - lw
        left.Resize(fyne.NewSize(lw, size.Height))
        left.Move(fyne.NewPos(0, 0))
        right.Resize(fyne.NewSize(rw, size.Height))
        right.Move(fyne.NewPos(lw, 0))
        return
    }
    // 3 objects: left, separator, right
    left := objs[0]
    sep := objs[1]
    right := objs[2]
    th := fyne.CurrentApp().Settings().Theme().Size(theme.SizeNameSeparatorThickness)
    avail := size.Width - th
    if avail < 0 { avail = 0 }
    lw := avail * l.ratio
    rw := avail - lw
    left.Resize(fyne.NewSize(lw, size.Height))
    left.Move(fyne.NewPos(0, 0))
    sep.Resize(fyne.NewSize(th, size.Height))
    sep.Move(fyne.NewPos(lw, 0))
    right.Resize(fyne.NewSize(rw, size.Height))
    right.Move(fyne.NewPos(lw+th, 0))
}

func (l fixedSplitLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
    if len(objs) < 2 {
        return fyne.NewSize(0, 0)
    }
    if len(objs) == 2 {
        m0 := objs[0].MinSize()
        m1 := objs[1].MinSize()
        h := m0.Height
        if m1.Height > h {
            h = m1.Height
        }
        return fyne.NewSize(m0.Width+m1.Width, h)
    }
    m0 := objs[0].MinSize()
    m1 := objs[1].MinSize()
    m2 := objs[2].MinSize()
    h := m0.Height
    if m1.Height > h { h = m1.Height }
    if m2.Height > h { h = m2.Height }
    return fyne.NewSize(m0.Width+m1.Width+m2.Width, h)
}

type Device struct {
    IP    string
    Port  string
    ID    string
}

func main() {
    a := app.New()
    lang := "zh" // default language: Chinese
    w := a.NewWindow(windowTitle(lang))
    w.Resize(fyne.NewSize(1024, 600))
    // Disable window resizing by user
    w.SetFixedSize(true)

    // Ensure Chinese font support: try system/env/assets; if none, prompt to download
    ensureCJKFont(a, w)

    // Loading managers for smooth UI transitions
    scanLoadingMgr := NewLoadingManager()
    queryLoadingMgr := NewLoadingManager()
    configLoadingMgr := NewLoadingManager()
    restartLoadingMgr := NewLoadingManager()

    // UI state
    devices := []Device{}
    selectedIndex := -1

    // Widgets: Left Table with 3 columns (ID, IP, PORT) - preset 5 rows for grid lines
    minRows := 5
    table := widget.NewTable(
        func() (int, int) { 
            rows := len(devices) + 1 // +1 for header
            if rows < minRows + 1 { rows = minRows + 1 } // ensure minimum rows for grid lines
            return rows, 3 
        },
        func() fyne.CanvasObject { return widget.NewLabel("") },
        func(id widget.TableCellID, o fyne.CanvasObject) {
            lbl := o.(*widget.Label)
            if id.Row == 0 {
                switch id.Col {
                case 0:
                    lbl.SetText("ID")
                case 1:
                    lbl.SetText("IP")
                case 2:
                    lbl.SetText("PORT")
                }
                return
            }
            if id.Row-1 < len(devices) {
                d := devices[id.Row-1]
                switch id.Col {
                case 0:
                    lbl.SetText(d.ID)
                case 1:
                    lbl.SetText(d.IP)
                case 2:
                    lbl.SetText(d.Port)
                }
            } else {
                // Empty rows for grid lines
                lbl.SetText("")
            }
        },
    )
    // Fix table layout: set reasonable column widths and row height
    table.SetColumnWidth(0, 220) // ID
    table.SetColumnWidth(1, 140) // IP
    table.SetColumnWidth(2, 80)  // PORT
    table.SetRowHeight(0, 28)
    // Right-side selected host & interface indicators (readable labels inside bordered groups)
    selectedIPLabel := widget.NewLabel("")
    selectedIfaceLabel := widget.NewLabel("")
    hostTitleLabel := widget.NewLabel(selectedHostTitle(lang))
    ifaceTitleLabel := widget.NewLabel(selectedIfaceLabelTitle(lang))
    hostCard := widget.NewCard("", "", container.NewVBox(hostTitleLabel, selectedIPLabel))
    ifaceCard := widget.NewCard("", "", container.NewVBox(ifaceTitleLabel, selectedIfaceLabel))
    // Predeclare config inputs and action buttons used in selection callback for autofill/state
    var newIPEntry *widget.Entry
    var queryBtn *widget.Button
    var applyBtn *widget.Button
    var viewBtn *widget.Button
    var restartBtn *widget.Button
    var reservedBtn2 *widget.Button
    var reservedBtn3 *widget.Button
    var hintLabel *widget.Label

    table.OnSelected = func(id widget.TableCellID) {
        if id.Row == 0 { // header row not selectable
            selectedIndex = -1
            selectedIPLabel.SetText("")
            if queryBtn != nil { queryBtn.Disable() }
            if applyBtn != nil { applyBtn.Disable() }
            if hintLabel != nil { hintLabel.Show() }
            return
        }
        idx := id.Row - 1
        if idx >= 0 && idx < len(devices) {
            selectedIndex = idx
            // Show selected host IP clearly
            selectedIPLabel.SetText(devices[idx].IP)
            // Auto-fill current known network parameters to config inputs
            newIPEntry.SetText(devices[idx].IP)
            // If device later supports reporting mask/gw/dns via protocol,
            // we can auto-fill them here.
            if queryBtn != nil { queryBtn.Enable() }
            if applyBtn != nil { applyBtn.Enable() }
            if restartBtn != nil { restartBtn.Enable() }
            // Pre-check device page availability on port 8000 before enabling View button
            if viewBtn != nil {
                viewBtn.Disable()
                ip := devices[idx].IP
                go func() {
                    online := isDevicePageOnline(ip, 1500*time.Millisecond)
                    if online { viewBtn.Enable() } else { viewBtn.Disable() }
                }()
            }
            // Keep hint area height stable: show empty text instead of hiding
            if hintLabel != nil { hintLabel.SetText(" ") }
    }
}
    table.OnUnselected = func(id widget.TableCellID) {
        if id.Row == 0 { return }
        selectedIndex = -1
        selectedIPLabel.SetText("")
        if queryBtn != nil { queryBtn.Disable() }
        if applyBtn != nil { applyBtn.Disable() }
        if viewBtn != nil { viewBtn.Disable() }
        if restartBtn != nil { restartBtn.Disable() }
        // Restore hint text (still shown to keep height stable)
        if hintLabel != nil { hintLabel.SetText(selectDevicePrompt(lang)) }
    }

    status := widget.NewLabel(statusReady(lang))
    
    // Connect status widget with loading managers for smooth transitions
    scanLoadingMgr.SetStatusWidget(status)
    queryLoadingMgr.SetStatusWidget(status)
    configLoadingMgr.SetStatusWidget(status)
    restartLoadingMgr.SetStatusWidget(status)

    // Discovery button
    scanBtn := widget.NewButtonWithIcon(scanButtonText(lang), theme.SearchIcon(), func() {
        scanLoadingMgr.StartLoading()
        scanLoadingMgr.UpdateStatus(statusScanning(lang))
        go func() {
            found, err := discover("60000", 2*time.Second)
            
            scanLoadingMgr.FinishLoading(func() {
                if err != nil {
                    scanLoadingMgr.UpdateStatus(scanError(lang) + err.Error())
                    return
                }
                devices = found
                table.Refresh()
                // reset current selection indicator after a fresh scan
                selectedIndex = -1
                selectedIPLabel.SetText("")
                if queryBtn != nil { queryBtn.Disable() }
                if applyBtn != nil { applyBtn.Disable() }
                if viewBtn != nil { viewBtn.Disable() }
                if hintLabel != nil { hintLabel.Show() }
                scanLoadingMgr.UpdateStatus(foundFmt(lang, len(devices)))
            })
        }()
    })
    scanBtn.Importance = widget.HighImportance

    // Config form
    cfgTitle := widget.NewLabel(cfgParamsTitle(lang))
    // Query network params button
    // New configuration inputs
    newIPEntry = widget.NewEntry()
    newIPEntry.SetPlaceHolder(newIPPlaceholder(lang))
    netmaskEntry := widget.NewEntry()
    netmaskEntry.SetPlaceHolder(netmaskPlaceholder(lang))
    gatewayEntry := widget.NewEntry()
    gatewayEntry.SetPlaceHolder(gatewayPlaceholder(lang))
    dnsEntry := widget.NewEntry()
    dnsEntry.SetPlaceHolder(dnsPlaceholder(lang))

    // Network mode select: static or dhcp
    modeSelect := widget.NewSelect([]string{"static", "dhcp"}, func(v string) {
        if strings.ToLower(v) == "dhcp" {
            newIPEntry.Disable()
            netmaskEntry.Disable()
            gatewayEntry.Disable()
            dnsEntry.Disable()
        } else {
            newIPEntry.Enable()
            netmaskEntry.Enable()
            gatewayEntry.Enable()
            dnsEntry.Enable()
        }
    })
    modeSelect.PlaceHolder = netModeLabel(lang)
    modeSelect.Selected = "static"

    queryBtn = widget.NewButtonWithIcon(queryNetButtonText(lang), theme.SearchIcon(), func() {
        if selectedIndex == -1 {
            status.SetText(selectDevicePrompt(lang))
            return
        }
        d := devices[selectedIndex]
        p := parsePort(d.Port, 60000)
        queryLoadingMgr.StartLoading()
        queryLoadingMgr.UpdateStatus(statusQuerying(lang))
        go func() {
            ip, mask, gw, dns, iface, err := queryNetParams(d.IP, p, 2*time.Second)
            
            queryLoadingMgr.FinishLoading(func() {
                if err != nil {
                    queryLoadingMgr.UpdateStatus(queryFailed(lang) + err.Error())
                    dialog.NewInformation(errorTitle(lang), queryFailed(lang)+err.Error(), w).Show()
                    return
                }
                // Autofill entries with returned values (only non-empty)
                if ip != "" { newIPEntry.SetText(ip) }
                if mask != "" { netmaskEntry.SetText(mask) }
                if gw != "" { gatewayEntry.SetText(gw) }
                if dns != "" { dnsEntry.SetText(dns) }
                if iface != "" { selectedIfaceLabel.SetText(iface) }
                queryLoadingMgr.UpdateStatus(queryFilled(lang))
            })
        }()
    })
    queryBtn.Importance = widget.HighImportance

    applyBtn = widget.NewButtonWithIcon(applyButtonText(lang), theme.UploadIcon(), func() {
        // Determine target: must be selected device (no broadcast)
        if selectedIndex == -1 {
            status.SetText(selectDevicePrompt(lang))
            return
        }
        d := devices[selectedIndex]
        p := parsePort(d.Port, 60000)
        // targetAddr := &net.UDPAddr{IP: net.ParseIP(d.IP), Port: p}

        // Validate inputs (if provided)
        ip := strings.TrimSpace(newIPEntry.Text)
        mask := strings.TrimSpace(netmaskEntry.Text)
        gw := strings.TrimSpace(gatewayEntry.Text)
        dns := strings.TrimSpace(dnsEntry.Text)

        isDHCP := strings.ToLower(modeSelect.Selected) == "dhcp"

        if !isDHCP {
            if ip == "" && mask == "" && gw == "" && dns == "" {
                status.SetText(noParamsProvided(lang))
                return
            }
            if ip != "" && !isValidIPv4(ip) {
                status.SetText(invalidIP(lang))
                dialog.NewInformation(errorTitle(lang), invalidIP(lang), w).Show()
                return
            }
            if mask != "" && !isValidIPv4(mask) {
                status.SetText(invalidNetmask(lang))
                dialog.NewInformation(errorTitle(lang), invalidNetmask(lang), w).Show()
                return
            }
            if gw != "" && !isValidIPv4(gw) {
                status.SetText(invalidGateway(lang))
                dialog.NewInformation(errorTitle(lang), invalidGateway(lang), w).Show()
                return
            }
            if dns != "" && !isValidIPv4(dns) {
                status.SetText(invalidDNS(lang))
                dialog.NewInformation(errorTitle(lang), invalidDNS(lang), w).Show()
                return
            }
        }
        // Confirm before sending
        dialog.NewConfirm(confirmSendConfigTitle(lang), confirmSendConfigMessage(lang), func(ok bool) {
            if !ok { return }
            msg := buildNetCfgWithMode(isDHCP, ip, mask, gw, dns)
            configLoadingMgr.StartLoading()
            configLoadingMgr.UpdateStatus(configSending(lang))
            go func() {
                ack, err := sendCfgAndWaitAck(d.IP, p, []byte(msg), 3*time.Second)
                configLoadingMgr.FinishLoading(func() {
                    if err != nil {
                        configLoadingMgr.UpdateStatus(sendFailed(lang) + err.Error())
                        dialog.NewInformation(errorTitle(lang), sendFailed(lang)+err.Error(), w).Show()
                        return
                    }
                    a := parseCfgAck(ack)
                    configLoadingMgr.UpdateStatus(a.StatusText(lang))
                    dialog.NewInformation(infoTitle(lang), a.PopupText(lang), w).Show()
                })
            }()
        }, w).Show()
    })
    applyBtn.Importance = widget.HighImportance
    // Initially disable buttons until a device is selected
    queryBtn.Disable()
    applyBtn.Disable()
    // Page view button: launches helper binary to open a 1280x800 fixed-size embedded webview
    viewBtn = widget.NewButton(viewButtonText(lang), func() {
        if selectedIndex == -1 {
            status.SetText(selectDevicePrompt(lang))
            return
        }
        d := devices[selectedIndex]
        urlStr := fmt.Sprintf("http://%s:8000", d.IP)
        exePath, _ := os.Executable()
        exeDir := filepath.Dir(exePath)
        viewerPath := filepath.Join(exeDir, "page_viewer")
        if _, err := os.Stat(viewerPath); err != nil {
            // Fallback: try PATH
            if p, e := exec.LookPath("page_viewer"); e == nil { viewerPath = p } else {
                dialog.NewInformation(errorTitle(lang), "page_viewer not found. Please build it.", w).Show()
                return
            }
        }
        cmd := exec.Command(viewerPath, urlStr, lang)
        if err := cmd.Start(); err != nil {
            dialog.NewInformation(errorTitle(lang), "Failed to launch page_viewer: "+err.Error(), w).Show()
            return
        }
    })
    // Make View Page button colored (primary style)
    viewBtn.Importance = widget.HighImportance
    viewBtn.Disable()
    // Restart button
    restartBtn = widget.NewButton(restartButtonText(lang), func() {
        if selectedIndex == -1 {
            status.SetText(selectDevicePrompt(lang))
            return
        }
        dialog.NewConfirm(confirmRestartTitle(lang), confirmRestartMessage(lang), func(ok bool) {
            if !ok { return }
            d := devices[selectedIndex]
            p := parsePort(d.Port, 60000)
            restartLoadingMgr.StartLoading()
            restartLoadingMgr.UpdateStatus(statusRestarting(lang))
            go func() {
                ack, err := sendRestartAndWaitAck(d.IP, p, 2*time.Second)
                restartLoadingMgr.FinishLoading(func() {
                    if err != nil {
                        restartLoadingMgr.UpdateStatus(restartFailedStatus(lang) + err.Error())
                        dialog.NewInformation(errorTitle(lang), restartFailedStatus(lang)+err.Error(), w).Show()
                        return
                    }
                    restartLoadingMgr.UpdateStatus(restartOKStatus(lang))
                    dialog.NewInformation(infoTitle(lang), restartOKPopup(lang)+"\n"+ack, w).Show()
                })
            }()
        }, w).Show()
    })
    restartBtn.Importance = widget.HighImportance
    restartBtn.Disable()

    // Reserved buttons (placeholders)
    reservedBtn2 = widget.NewButton(reservedButtonText2(lang), func() {})
    reservedBtn2.Disable()
    reservedBtn3 = widget.NewButton(reservedButtonText3(lang), func() {})
    reservedBtn3.Disable()
    // Hint shown when no device is selected (left-aligned, subtle)
    hintLabel = widget.NewLabelWithStyle(selectDevicePrompt(lang), fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
    hintLabel.Alignment = fyne.TextAlignLeading
    hintLabel.Show()

    // Form: two bordered groups side-by-side for clearer visual separation
    infoRow := container.NewGridWithColumns(2,
        hostCard,
        ifaceCard,
    )
    form := container.NewVBox(
        cfgTitle,
        infoRow,
        container.NewGridWithColumns(2,
            widget.NewLabel(netModeLabel(lang)),
            modeSelect,
        ),
        newIPEntry,
        netmaskEntry,
        gatewayEntry,
        dnsEntry,
    )

    // Settings button
    var settingsBtn *widget.Button
    settingsBtn = widget.NewButtonWithIcon(settingsText(lang), theme.SettingsIcon(), func() {
        // Build settings content
        langSelect := widget.NewSelect([]string{"English", "中文"}, func(value string) {})
        if lang == "zh" { langSelect.SetSelected("中文") } else { langSelect.SetSelected("English") }

        loadFontBtn := widget.NewButton(loadFontText(lang), func() {
            fd := dialog.NewFileOpen(func(uc fyne.URIReadCloser, err error) {
                if err != nil || uc == nil { return }
                p := uc.URI().Path()
                if !isSupportedFontExt(p) { dialog.NewInformation(infoTitle(lang), invalidFont(lang), w).Show(); return }
                if b, e := ioReadAll(uc); e == nil && len(b) > 0 {
                    a.Settings().SetTheme(newCJKTheme(fyne.NewStaticResource(filepath.Base(p), b)))
                    dialog.NewInformation(infoTitle(lang), fontApplied(lang), w).Show()
                }
            }, w)
            fd.SetFilter(storage.NewExtensionFileFilter([]string{".ttf", ".otf"}))
            fd.Show()
        })

        useSystemFontBtn := widget.NewButton(useSystemFontText(lang), func() {
            if applied := tryApplyAnyCJK(a); applied {
                dialog.NewInformation(infoTitle(lang), fontApplied(lang), w).Show()
            } else {
                dialog.NewInformation(infoTitle(lang), noFontFound(lang), w).Show()
            }
        })

        content := container.NewVBox(
            widget.NewLabel(languageLabel(lang)),
            langSelect,
            loadFontBtn,
            useSystemFontBtn,
        )
        dialog.NewCustomConfirm(settingsText(lang), okText(lang), cancelText(lang), content, func(ok bool) {
            if !ok { return }
            // Apply language and refresh texts
            sel := langSelect.Selected
            if sel == "中文" { lang = "zh" } else { lang = "en" }
            // Update texts
            w.SetTitle(windowTitle(lang))
            status.SetText(statusReady(lang))
            scanBtn.SetText(scanButtonText(lang))
            cfgTitle.SetText(cfgParamsTitle(lang))
            queryBtn.SetText(queryNetButtonText(lang))
            hostTitleLabel.SetText(selectedHostTitle(lang))
            ifaceTitleLabel.SetText(selectedIfaceLabelTitle(lang))
            newIPEntry.SetPlaceHolder(newIPPlaceholder(lang))
            netmaskEntry.SetPlaceHolder(netmaskPlaceholder(lang))
            gatewayEntry.SetPlaceHolder(gatewayPlaceholder(lang))
            dnsEntry.SetPlaceHolder(dnsPlaceholder(lang))
            applyBtn.SetText(applyButtonText(lang))
            settingsBtn.SetText(settingsText(lang))
            viewBtn.SetText(viewButtonText(lang))
            restartBtn.SetText(restartButtonText(lang))
            reservedBtn2.SetText(reservedButtonText2(lang))
            reservedBtn3.SetText(reservedButtonText3(lang))
            if hintLabel != nil { hintLabel.SetText(selectDevicePrompt(lang)) }
        }, w).Show()
    })
    settingsBtn.Importance = widget.HighImportance

    // Right pane: buttons at bottom with a small hint below, left-aligned
    btnRow := container.NewGridWithColumns(3, queryBtn, applyBtn, viewBtn)
    extraRow := container.NewGridWithColumns(3, restartBtn, reservedBtn2, reservedBtn3)
    btnBlock := container.NewVBox(btnRow, extraRow, hintLabel)
    rightPane := container.NewBorder(nil, btnBlock, nil, nil, form)

    // Use a custom fixed ratio split layout with a vertical separator for 66%/34%
    sep := widget.NewSeparator()
    split := container.New(fixedSplitLayout{ratio: 0.66}, table, sep, rightPane)

    // Top buttons left-aligned, equal minimal widths
    btnH := scanBtn.MinSize().Height
    btnW := scanBtn.MinSize().Width
    if w := settingsBtn.MinSize().Width; w > btnW { btnW = w }
    btnW += 8 // small padding to keep width roughly unchanged
    topBar := container.NewGridWrap(fyne.NewSize(btnW, btnH), scanBtn, settingsBtn)

    // Keep status at the bottom of the whole window
    content := container.NewBorder(
        topBar,
        status,
        nil,
        nil,
        split,
    )

    w.SetContent(content)
    w.ShowAndRun()
}

// ---- i18n helpers ----
func windowTitle(lang string) string            { if lang == "zh" { return "设备发现与配置" } ; return "Device Discovery & Config" }
func statusReady(lang string) string            { if lang == "zh" { return "就绪" } ; return "Ready" }
func broadcastLabel(lang string) string         { if lang == "zh" { return "广播配置模式" } ; return "Broadcast config mode" }
func scanButtonText(lang string) string         { if lang == "zh" { return "扫描设备" } ; return "Scan Devices" }
func statusScanning(lang string) string         { if lang == "zh" { return "扫描中..." } ; return "Scanning..." }
func statusQuerying(lang string) string         { if lang == "zh" { return "查询网络参数中..." } ; return "Querying network params..." }
func queryFailed(lang string) string            { if lang == "zh" { return "查询失败: " } ; return "Query failed: " }
func queryFilled(lang string) string            { if lang == "zh" { return "已填充当前网络参数" } ; return "Filled current network params" }
func scanError(lang string) string              { if lang == "zh" { return "扫描错误: " } ; return "Scan error: " }
func foundFmt(lang string, n int) string        { if lang == "zh" { return fmt.Sprintf("发现 %d 台设备", n) } ; return fmt.Sprintf("Found %d device(s)", n) }
func cfgParamsTitle(lang string) string         { if lang == "zh" { return "配置参数 (CFG|ID=..|IP=..|PORT=..):" } ; return "Config params (CFG|ID=..|IP=..|PORT=..):" }
// New GUI i18n for targeted config
func selectedHostTitle(lang string) string      { if lang == "zh" { return "选中主机 (只读)" } ; return "Selected Host (read-only)" }
func selectedHostPlaceholder(lang string) string{ if lang == "zh" { return "左侧选择主机后显示其IP" } ; return "IP of selected host" }
func selectedIfaceLabelTitle(lang string) string{ if lang == "zh" { return "网卡名称 (只读)" } ; return "Interface Name (read-only)" }
func newIPPlaceholder(lang string) string       { if lang == "zh" { return "新IP，例如 192.168.1.10" } ; return "New IP, e.g. 192.168.1.10" }
func netmaskPlaceholder(lang string) string     { if lang == "zh" { return "掩码，例如 255.255.255.0" } ; return "Netmask, e.g. 255.255.255.0" }
func gatewayPlaceholder(lang string) string     { if lang == "zh" { return "网关，例如 192.168.1.1" } ; return "Gateway, e.g. 192.168.1.1" }
func dnsPlaceholder(lang string) string         { if lang == "zh" { return "DNS，例如 8.8.8.8" } ; return "DNS, e.g. 8.8.8.8" }
func netModeLabel(lang string) string          { if lang == "zh" { return "网络模式" } ; return "Network Mode" }
func invalidIP(lang string) string              { if lang == "zh" { return "IP格式不正确" } ; return "Invalid IP format" }
func invalidNetmask(lang string) string         { if lang == "zh" { return "掩码格式不正确" } ; return "Invalid netmask format" }
func invalidGateway(lang string) string         { if lang == "zh" { return "网关格式不正确" } ; return "Invalid gateway format" }
func invalidDNS(lang string) string             { if lang == "zh" { return "DNS格式不正确" } ; return "Invalid DNS format" }
func noParamsProvided(lang string) string       { if lang == "zh" { return "请至少填写一个参数 (IP/掩码/网关/DNS)" } ; return "Provide at least one of IP/Netmask/Gateway/DNS" }
func applyButtonText(lang string) string        { if lang == "zh" { return "发送配置" } ; return "Send Config" }
func queryNetButtonText(lang string) string     { if lang == "zh" { return "参数详情" } ; return "Params Detail" }
func selectDevicePrompt(lang string) string     { if lang == "zh" { return "请先从左侧列表选择目标主机" } ; return "Select a device from the left list first" }
func configSending(lang string) string          { if lang == "zh" { return "正在发送配置并等待ACK..." } ; return "Sending config and waiting ACK..." }
// ACK semantics status lines
func cfgAckNetWrittenRestartOKStatus(lang string) string     { if lang == "zh" { return "写入成功，网络服务重启成功" } ; return "Written OK, network service restart OK" }
func cfgAckNetWrittenRestartFailedStatus(lang string) string { if lang == "zh" { return "写入成功，但网络服务重启失败" } ; return "Written OK, but network service restart failed" }
func cfgAckNetWriteFailedStatus(lang string) string          { if lang == "zh" { return "网络参数写入失败" } ; return "Network params write failed" }
func cfgAckSavedOnlyStatus(lang string) string               { if lang == "zh" { return "仅保存到本地配置（未包含网络参数）" } ; return "Saved to local config only (no network params)" }
// ACK semantics popup lines
func cfgAckNetWrittenRestartOKPopup(lang string) string     { if lang == "zh" { return "写入成功且重启成功：CFG_ACK|NET_ACK|RESTART_ACK" } ; return "Write OK and restart OK: CFG_ACK|NET_ACK|RESTART_ACK" }
func cfgAckNetWrittenRestartFailedPopup(lang string) string { if lang == "zh" { return "写入成功但重启失败：CFG_ACK|NET_ACK|RESTART_NACK" } ; return "Write OK but restart failed: CFG_ACK|NET_ACK|RESTART_NACK" }
func cfgAckNetWriteFailedPopup(lang string) string          { if lang == "zh" { return "写入失败：CFG_ACK|NET_NACK" } ; return "Write failed: CFG_ACK|NET_NACK" }

// Restart i18n
func restartButtonText(lang string) string          { if lang == "zh" { return "重启主机" } ; return "Restart Host" }
func statusRestarting(lang string) string           { if lang == "zh" { return "正在发送重启指令..." } ; return "Sending restart command..." }
func restartOKStatus(lang string) string            { if lang == "zh" { return "重启指令已确认" } ; return "Restart acknowledged" }
func restartFailedStatus(lang string) string        { if lang == "zh" { return "重启失败或未收到ACK：" } ; return "Restart failed or no ACK: " }
func restartOKPopup(lang string) string             { if lang == "zh" { return "设备已返回RESTART_ACK" } ; return "Device returned RESTART_ACK" }
func reservedButtonText2(lang string) string        { if lang == "zh" { return "预留2" } ; return "Reserved 2" }
func reservedButtonText3(lang string) string        { if lang == "zh" { return "预留3" } ; return "Reserved 3" }
func cfgAckSavedOnlyPopup(lang string) string               { if lang == "zh" { return "仅保存到本地：CFG_ACK|ID=<id>" } ; return "Saved to local only: CFG_ACK|ID=<id>" }
func sendFailed(lang string) string             { if lang == "zh" { return "发送失败: " } ; return "Send failed: " }
func configSent(lang string) string             { if lang == "zh" { return "已发送配置: " } ; return "Config sent: " }
func settingsText(lang string) string           { if lang == "zh" { return "设置" } ; return "Settings" }
func languageLabel(lang string) string          { if lang == "zh" { return "语言" } ; return "Language" }
func loadFontText(lang string) string           { if lang == "zh" { return "从文件加载字体" } ; return "Load font from file" }
func useSystemFontText(lang string) string      { if lang == "zh" { return "使用系统中文字体" } ; return "Use system CJK font" }
func infoTitle(lang string) string              { if lang == "zh" { return "提示" } ; return "Info" }
func invalidFont(lang string) string            { if lang == "zh" { return "请选择 .ttf/.otf 字体文件" } ; return "Please choose a .ttf/.otf font file" }
func fontApplied(lang string) string            { if lang == "zh" { return "字体已应用" } ; return "Font applied" }
func noFontFound(lang string) string            { if lang == "zh" { return "未检测到可用中文字体" } ; return "No system CJK font found" }
func okText(lang string) string                 { if lang == "zh" { return "确定" } ; return "OK" }
func cancelText(lang string) string             { if lang == "zh" { return "取消" } ; return "Cancel" }
func errorTitle(lang string) string             { if lang == "zh" { return "错误" } ; return "Error" }
func viewButtonText(lang string) string         { if lang == "zh" { return "页面查看" } ; return "Open Web Page" }
func viewWindowTitle(lang string) string        { if lang == "zh" { return "设备网页" } ; return "Device Web Page" }
// Confirm dialogs
func confirmSendConfigTitle(lang string) string   { if lang == "zh" { return "确认发送配置" } ; return "Confirm Send Config" }
func confirmSendConfigMessage(lang string) string { if lang == "zh" { return "确定要将网络配置发送到该设备吗？" } ; return "Are you sure to send network config to this device?" }
func confirmRestartTitle(lang string) string      { if lang == "zh" { return "确认重启" } ; return "Confirm Restart" }
func confirmRestartMessage(lang string) string    { if lang == "zh" { return "确定要重启该设备吗？" } ; return "Are you sure to restart the device?" }
func openingBrowserText(lang string) string     { if lang == "zh" { return "正在使用浏览器访问所选设备网页" } ; return "Opening device web page in browser" }

// helper to read all from URIReadCloser (since io.ReadAll requires import)
func ioReadAll(uc fyne.URIReadCloser) ([]byte, error) {
    defer uc.Close()
    b := make([]byte, 0)
    buf := make([]byte, 4096)
    for {
        n, err := uc.Read(buf)
        if n > 0 {
            b = append(b, buf[:n]...)
        }
        if err != nil {
            if err.Error() == "EOF" { return b, nil }
            return b, nil // Fyne URIReadCloser may not return EOF; best-effort
        }
    }
}

// Legacy builder (kept if needed elsewhere)
func buildCfg(id, ip, port string) string {
    parts := []string{"CFG"}
    if strings.TrimSpace(id) != "" { parts = append(parts, "ID="+strings.TrimSpace(id)) }
    if strings.TrimSpace(ip) != "" { parts = append(parts, "IP="+strings.TrimSpace(ip)) }
    if strings.TrimSpace(port) != "" { parts = append(parts, "PORT="+strings.TrimSpace(port)) }
    return strings.Join(parts, "|")
}

// New builder for IP parameters
func buildNetCfg(ip, mask, gw, dns string) string {
    parts := []string{"CFG"}
    if strings.TrimSpace(ip) != "" { parts = append(parts, "IP="+strings.TrimSpace(ip)) }
    if strings.TrimSpace(mask) != "" { parts = append(parts, "MASK="+strings.TrimSpace(mask)) }
    if strings.TrimSpace(gw) != "" { parts = append(parts, "GW="+strings.TrimSpace(gw)) }
    if strings.TrimSpace(dns) != "" { parts = append(parts, "DNS="+strings.TrimSpace(dns)) }
    return strings.Join(parts, "|")
}

// Builder that includes DHCP mode when selected
func buildNetCfgWithMode(dhcp bool, ip, mask, gw, dns string) string {
    if dhcp {
        return "CFG|DHCP=1"
    }
    return buildNetCfg(ip, mask, gw, dns)
}

// Simple IPv4 validation
func isValidIPv4(s string) bool {
    ip := net.ParseIP(strings.TrimSpace(s))
    return ip != nil && ip.To4() != nil
}

// Query NET params from a target IP:PORT within timeout
// Returns IP, MASK, GW, DNS, and optional interface name (e.g., eth0)
func queryNetParams(ip string, port int, timeout time.Duration) (rip, mask, gw, dns, iface string, err error) {
    conn, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
    if e != nil { return "", "", "", "", "", e }
    defer conn.Close()
    _ = conn.SetDeadline(time.Now().Add(timeout))
    raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
    if _, e = conn.WriteToUDP([]byte("QUERY_NET"), raddr); e != nil {
        return "", "", "", "", "", e
    }
    buf := make([]byte, 2048)
    for {
        n, from, e := conn.ReadFromUDP(buf)
        if e != nil { return "", "", "", "", "", e }
        msg := strings.TrimSpace(string(buf[:n]))
        // Accept reply only from target host
        if addrIP(from) != ip { continue }
        // Accept different NET reply prefixes, e.g., NET|..., NET_IF|...
        upper := strings.ToUpper(msg)
        if strings.HasPrefix(upper, "NET|") || strings.HasPrefix(upper, "NET_IF|") || strings.HasPrefix(upper, "NET ") || strings.HasPrefix(upper, "NET") {
            rip, mask, gw, dns, iface = parseNetResponse(msg)
            return rip, mask, gw, dns, iface, nil
        }
    }
}

// Parse NET|IP=...|MASK=...|GW=...|DNS=...|IF=eth0 (or IFACE=eth0)
func parseNetResponse(msg string) (ip, mask, gw, dns, iface string) {
    parts := strings.Split(msg, "|")
    // tolerate different prefixes like NET_IF
    start := 1
    if len(parts) > 0 && (strings.HasPrefix(strings.ToUpper(parts[0]), "NET") || strings.HasPrefix(strings.ToUpper(parts[0]), "NET_IF")) {
        start = 1
    } else {
        start = 0
    }
    for _, p := range parts[start:] {
        kv := strings.SplitN(p, "=", 2)
        if len(kv) != 2 { continue }
        k := strings.ToUpper(strings.TrimSpace(kv[0]))
        v := strings.TrimSpace(kv[1])
        switch k {
        case "IP":
            ip = v
        case "MASK":
            mask = v
        case "GW":
            gw = v
        case "DNS":
            dns = v
        case "IF":
            iface = v
        case "IFACE":
            iface = v
        case "ETH":
            iface = v
        case "NIC":
            iface = v
        case "DEV":
            iface = v
        case "INTERFACE":
            iface = v
        case "IFNAME":
            iface = v
        }
    }
    return
}

// sendCfgAndWaitAck sends CFG payload to ip:port and waits for CFG_ACK
func sendCfgAndWaitAck(ip string, port int, payload []byte, timeout time.Duration) (string, error) {
    conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
    if err != nil { return "", err }
    defer conn.Close()
    _ = conn.SetDeadline(time.Now().Add(timeout))
    raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
    if _, err = conn.WriteToUDP(payload, raddr); err != nil {
        return "", err
    }
    buf := make([]byte, 2048)
    for {
        n, from, err := conn.ReadFromUDP(buf)
        if err != nil { return "", err }
        if addrIP(from) != ip { continue }
        msg := strings.TrimSpace(string(buf[:n]))
        if strings.HasPrefix(strings.ToUpper(msg), "CFG_ACK") {
            return msg, nil
        }
    }
}

// sendRestartAndWaitAck sends RESTART to ip:port and waits for RESTART_ACK
func sendRestartAndWaitAck(ip string, port int, timeout time.Duration) (string, error) {
    conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
    if err != nil { return "", err }
    defer conn.Close()
    _ = conn.SetDeadline(time.Now().Add(timeout))
    raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
    payload := []byte("RESTART")
    if _, err = conn.WriteToUDP(payload, raddr); err != nil {
        return "", err
    }
    buf := make([]byte, 2048)
    for {
        n, from, err := conn.ReadFromUDP(buf)
        if err != nil { return "", err }
        if addrIP(from) != ip { continue }
        msg := strings.TrimSpace(string(buf[:n]))
        up := strings.ToUpper(msg)
        if strings.Contains(up, "RESTART_ACK") {
            return msg, nil
        }
        if strings.HasPrefix(up, "CFG_ACK") && strings.Contains(up, "RESTART_ACK") {
            return msg, nil
        }
    }
}

type cfgAck struct{
    HasNetAck bool
    HasNetNack bool
    HasRestartAck bool
    HasRestartNack bool
}

func parseCfgAck(msg string) cfgAck {
    up := strings.ToUpper(msg)
    return cfgAck{
        HasNetAck: strings.Contains(up, "NET_ACK"),
        HasNetNack: strings.Contains(up, "NET_NACK"),
        HasRestartAck: strings.Contains(up, "RESTART_ACK"),
        HasRestartNack: strings.Contains(up, "RESTART_NACK"),
    }
}

func (c cfgAck) StatusText(lang string) string {
    if c.HasNetNack {
        return cfgAckNetWriteFailedStatus(lang)
    }
    if c.HasNetAck && c.HasRestartAck {
        return cfgAckNetWrittenRestartOKStatus(lang)
    }
    if c.HasNetAck && c.HasRestartNack {
        return cfgAckNetWrittenRestartFailedStatus(lang)
    }
    return cfgAckSavedOnlyStatus(lang)
}

func (c cfgAck) PopupText(lang string) string {
    if c.HasNetNack {
        return cfgAckNetWriteFailedPopup(lang)
    }
    if c.HasNetAck && c.HasRestartAck {
        return cfgAckNetWrittenRestartOKPopup(lang)
    }
    if c.HasNetAck && c.HasRestartNack {
        return cfgAckNetWrittenRestartFailedPopup(lang)
    }
    return cfgAckSavedOnlyPopup(lang)
}

func sendUDP(network string, laddr, raddr *net.UDPAddr, payload []byte) error {
    conn, err := net.DialUDP(network, laddr, raddr)
    if err != nil {
        return err
    }
    defer conn.Close()
    _, err = conn.Write(payload)
    return err
}

func discover(port string, timeout time.Duration) ([]Device, error) {
    // Use a single UDP socket to send broadcast and receive replies on the same port
    conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
    if err != nil {
        return nil, err
    }
    defer conn.Close()
    _ = conn.SetDeadline(time.Now().Add(timeout))

    baddr := &net.UDPAddr{IP: net.IPv4bcast, Port: parsePort(port, 60000)}
    if _, err := conn.WriteToUDP([]byte("TF"), baddr); err != nil {
        return nil, err
    }

    buf := make([]byte, 2048)
    found := map[string]Device{}
    for {
        n, from, err := conn.ReadFromUDP(buf)
        if err != nil {
            // Deadline or other error ends discovery
            break
        }
        msg := strings.TrimSpace(string(buf[:n]))
        if strings.HasPrefix(strings.ToUpper(msg), "TF|") {
            d := parseDiscovery(from, msg)
            key := from.String()
            found[key] = d
        }
    }
    // Convert map to slice
    out := make([]Device, 0, len(found))
    for _, d := range found {
        out = append(out, d)
    }
    return out, nil
}

func parseDiscovery(from net.Addr, msg string) Device {
    d := Device{IP: addrIP(from), Port: "", ID: ""}
    // Message format: TF|ID=<id>|PORT=<port>
    parts := strings.Split(msg, "|")
    for _, p := range parts[1:] { // skip "TF"
        kv := strings.SplitN(p, "=", 2)
        if len(kv) != 2 { continue }
        k := strings.ToUpper(strings.TrimSpace(kv[0]))
        v := strings.TrimSpace(kv[1])
        switch k {
        case "ID":
            d.ID = v
        case "PORT":
            d.Port = v
        }
    }
    if d.Port == "" { d.Port = "60000" }
    return d
}

func addrIP(a net.Addr) string {
    s := a.String()
    if i := strings.LastIndex(s, ":"); i > 0 {
        return s[:i]
    }
    return s
}

func parsePort(s string, def int) int {
    if s == "" { return def }
    var p int
    _, err := fmt.Sscanf(s, "%d", &p)
    if err != nil || p <= 0 || p > 65535 { return def }
    return p
}

// isDevicePageOnline checks whether http://<ip>:8000 is reachable and returns 2xx/3xx
func isDevicePageOnline(ip string, timeout time.Duration) bool {
    if ip == "" { return false }
    url := fmt.Sprintf("http://%s:8000/", ip)
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    client := &http.Client{ Timeout: timeout }
    resp, err := client.Do(req)
    if err != nil {
        return false
    }
    defer resp.Body.Close()
    return resp.StatusCode >= 200 && resp.StatusCode < 400
}