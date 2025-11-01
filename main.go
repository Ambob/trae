package main

import (
    "encoding/json"
    "log"
    "net"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "strconv"
    "time"
)

// Simple UDP responder:
// - Listens on UDP port 60000 (default)
// - When receiving broadcast or direct UDP with content "TF" (case-insensitive), replies with "TF_ACK"
// - Otherwise replies with "UNKNOWN_CMD"
type DeviceConfig struct {
    ID    string `json:"id"`
    IP    string `json:"ip"`
    Port  string `json:"port"`
}

func main() {
    port := "60000"
    if p := os.Getenv("UDP_PORT"); p != "" {
        port = p
    }

    // Device ID from env or hostname fallback
    deviceID := os.Getenv("DEVICE_ID")
    if deviceID == "" {
        hn, _ := os.Hostname()
        if hn == "" {
            hn = "unknown"
        }
        deviceID = "HOST-" + hn
    }

    addr := ":" + port
    // Use IPv4 UDP; broadcast messages are received transparently by a normal listener.
    pc, err := net.ListenPacket("udp4", addr)
    if err != nil {
        log.Fatalf("failed to listen on UDP %s: %v", addr, err)
    }
    defer pc.Close()

    log.Printf("UDP responder listening on %s", addr)

    buf := make([]byte, 2048)
    for {
        n, remoteAddr, err := pc.ReadFrom(buf)
        if err != nil {
            // Continue on read errors to keep the server alive.
            log.Printf("read error: %v", err)
            continue
        }

        msg := strings.TrimSpace(string(buf[:n]))
        log.Printf("received from %s: %q", remoteAddr.String(), msg)

        var resp string
        switch {
        case strings.EqualFold(msg, "TF"):
            // Respond with discovery info: ID (from /etc/unique_ID, create if missing) and PORT
            uid, err := ensureUniqueID()
            if err != nil {
                log.Printf("ensureUniqueID error: %v")
            }
            resp = "TF|ID=" + uid + "|PORT=" + port
        case strings.EqualFold(msg, "GET_ID"):
            // Query unique ID from /etc/unique_ID; create if missing per rule.
            id, err := ensureUniqueID()
            if err != nil {
                log.Printf("ensureUniqueID error: %v")
            }
            resp = "ID=" + id
        case strings.EqualFold(msg, "QUERY") || strings.EqualFold(msg, "QRY") || strings.EqualFold(msg, "QUERY_NET") || strings.EqualFold(msg, "QRY_NET") || strings.EqualFold(msg, "NET") || strings.EqualFold(msg, "GET_NET"):
            // Query current network parameters (IP/MASK/GW/DNS)
            ip, mask, gw, dns := getNetworkParams()
            parts := []string{"NET"}
            if ip != "" { parts = append(parts, "IP="+ip) }
            if mask != "" { parts = append(parts, "MASK="+mask) }
            if gw != "" { parts = append(parts, "GW="+gw) }
            if dns != "" { parts = append(parts, "DNS="+dns) }
            // Append interface name (always include IF=..., with robust fallback)
            ifn := ifaceName()
            if ifn == "" { ifn = "eth0" }
            // Include both IF and IFACE for maximum client compatibility
            parts = append(parts, "IF="+ifn)
            parts = append(parts, "IFACE="+ifn)
            resp = strings.Join(parts, "|")
        case strings.HasPrefix(strings.ToUpper(msg), "CFG|"):
            // Parse simple key=value pairs separated by '|'
            cfg := parseConfig(msg)
            if cfg.ID == "" {
                // If no ID supplied, assume this device
                cfg.ID = deviceID
            }
            // Persist config to local JSON file (safe alternative to changing OS network settings)
            if err := saveConfig(cfg); err != nil {
                log.Printf("config save error: %v")
                resp = "CFG_NACK|ERR=SAVE_FAILED"
            } else {
                // Additionally, apply network changes:
                // - If DHCP flag present, write DHCP config to /etc/systemd/network/eth*.network
                // - Else if IP/MASK/GW/DNS present, write static config
                // Note: do NOT restart systemd-networkd to avoid potential connectivity loss.
                if hasDHCPFlag(msg) {
                    if err := applySystemdNetworkDHCP(); err != nil {
                        log.Printf("apply DHCP network config error: %v")
                        resp = "CFG_ACK|ID=" + cfg.ID + "|NET_NACK"
                    } else {
                        resp = "CFG_ACK|ID=" + cfg.ID + "|NET_ACK"
                    }
                } else {
                    ip, mask, gw, dns := parseNetKV(msg)
                    if ip != "" || mask != "" || gw != "" || dns != "" {
                        if err := applySystemdNetworkConfig(ip, mask, gw, dns); err != nil {
                            log.Printf("apply systemd network config error: %v")
                            resp = "CFG_ACK|ID=" + cfg.ID + "|NET_NACK"
                        } else {
                            // Do not restart systemd-networkd per current safety requirement
                            resp = "CFG_ACK|ID=" + cfg.ID + "|NET_ACK"
                        }
                    } else {
                        resp = "CFG_ACK|ID=" + cfg.ID
                    }
                }
            }
        case strings.EqualFold(msg, "RESTART"):
            // Attempt to restart the host; requires appropriate permissions on device side
            if err := restartHost(); err != nil {
                log.Printf("restart host error: %v", err)
                resp = "RESTART_NACK|ERR=" + strings.ReplaceAll(err.Error(), "|", ":")
            } else {
                resp = "RESTART_ACK"
            }
        default:
            resp = "UNKNOWN_CMD"
        }

        if _, err := pc.WriteTo([]byte(resp), remoteAddr); err != nil {
            log.Printf("write error to %s: %v", remoteAddr.String(), err)
        } else {
            log.Printf("responded to %s: %q", remoteAddr.String(), resp)
        }
    }
}

// parseConfig parses a message like: CFG|ID=abc|IP=192.168.1.10|PORT=60000
func parseConfig(s string) DeviceConfig {
    // Normalize and strip leading CFG|
    up := s
    if strings.HasPrefix(strings.ToUpper(up), "CFG|") {
        up = s[len("CFG|"):]
    }
    parts := strings.Split(up, "|")
    cfg := DeviceConfig{}
    for _, p := range parts {
        kv := strings.SplitN(p, "=", 2)
        if len(kv) != 2 {
            continue
        }
        k := strings.ToUpper(strings.TrimSpace(kv[0]))
        v := strings.TrimSpace(kv[1])
        switch k {
        case "ID":
            cfg.ID = v
        case "IP":
            cfg.IP = v
        case "PORT":
            cfg.Port = v
        }
    }
    return cfg
}

// parseNetKV extracts IP/MASK/GW/DNS from a payload like: CFG|IP=...|MASK=...|GW=...|DNS=...
func parseNetKV(s string) (ip, mask, gw, dns string) {
    // strip leading token up to first '|'
    idx := strings.Index(s, "|")
    rest := s
    if idx >= 0 {
        rest = s[idx+1:]
    }
    parts := strings.Split(rest, "|")
    for _, p := range parts {
        kv := strings.SplitN(p, "=", 2)
        if len(kv) != 2 { continue }
        k := strings.ToUpper(strings.TrimSpace(kv[0]))
        v := strings.TrimSpace(kv[1])
        switch k {
        case "IP": ip = v
        case "MASK": mask = v
        case "GW": gw = v
        case "DNS": dns = v
        }
    }
    return
}

// hasDHCPFlag detects DHCP intent in the CFG payload (e.g., CFG|DHCP=1 or DHCP=yes)
func hasDHCPFlag(s string) bool {
    up := strings.ToUpper(s)
    return strings.Contains(up, "DHCP=1") || strings.Contains(up, "DHCP=YES") || strings.Contains(up, "DHCP=TRUE")
}

// applySystemdNetworkConfig writes IP/mask/gateway/DNS to /etc/systemd/network/eth*.network
// It updates existing keys in [Network] section or creates a new file if none exists.
func applySystemdNetworkConfig(ip, mask, gw, dns string) error {
    dir := "/etc/systemd/network"
    // choose target file: prefer existing eth*.network else fallback to eth0.network
    matches, _ := filepath.Glob(filepath.Join(dir, "eth*.network"))
    var path string
    if len(matches) > 0 {
        path = matches[0]
    } else {
        // Try to infer iface name from default route
        iface := defaultIfaceFromProcRoute()
        if iface == "" || !strings.HasPrefix(iface, "eth") {
            iface = "eth0"
        }
        path = filepath.Join(dir, iface+".network")
    }

    // Read existing content if present
    var lines []string
    if b, err := os.ReadFile(path); err == nil {
        lines = strings.Split(string(b), "\n")
    } else {
        // Create a minimal template
        iface := "eth0"
        if d := defaultIfaceFromProcRoute(); d != "" && strings.HasPrefix(d, "eth") { iface = d }
        lines = []string{
            "[Match]",
            "Name=" + iface,
            "",
            "[Network]",
        }
    }

    // Ensure [Network] section exists
    hasNetwork := false
    for _, l := range lines {
        if strings.TrimSpace(l) == "[Network]" { hasNetwork = true; break }
    }
    if !hasNetwork {
        lines = append(lines, "", "[Network]")
    }

    // Build desired keys
    var addrLine string
    if ip != "" {
        if mask != "" {
            pfx := maskToPrefix(mask)
            if pfx > 0 { addrLine = "Address=" + ip + "/" + strconv.Itoa(pfx) } else { addrLine = "Address=" + ip }
        } else {
            addrLine = "Address=" + ip
        }
    }
    gwLine := ""
    if gw != "" { gwLine = "Gateway=" + gw }
    dnsLine := ""
    if dns != "" { dnsLine = "DNS=" + dns }

    // Update or append within [Network]
    lines = upsertInSection(lines, "[Network]", "Address=", addrLine)
    lines = upsertInSection(lines, "[Network]", "Gateway=", gwLine)
    lines = upsertInSection(lines, "[Network]", "DNS=", dnsLine)
    // Ensure DHCP disabled for static configuration
    lines = upsertInSection(lines, "[Network]", "DHCP=", "DHCP=no")

    // Write back
    content := strings.Join(lines, "\n")
    return os.WriteFile(path, []byte(content), 0o644)
}

// applySystemdNetworkDHCP writes a minimal DHCP config to /etc/systemd/network/eth*.network
// It chooses an existing eth*.network or falls back to <iface>.network based on default route.
func applySystemdNetworkDHCP() error {
    dir := "/etc/systemd/network"
    matches, _ := filepath.Glob(filepath.Join(dir, "eth*.network"))
    var path string
    var iface string
    if len(matches) > 0 {
        path = matches[0]
        // try infer iface from file name
        base := filepath.Base(path)
        if strings.HasPrefix(base, "eth") { iface = strings.TrimSuffix(base, ".network") }
    }
    if path == "" {
        // Pick default iface from route if available
        iface = defaultIfaceFromProcRoute()
        if iface == "" || !strings.HasPrefix(iface, "eth") {
            iface = "eth0"
        }
        path = filepath.Join(dir, iface+".network")
    }

    // Minimal DHCP file content
    lines := []string{
        "[Match]",
        "Name=" + iface,
        "",
        "[Network]",
        "DHCP=yes",
    }
    content := strings.Join(lines, "\n")
    return os.WriteFile(path, []byte(content), 0o644)
}

// upsertInSection finds a section header, and replaces the first line starting with keyPrefix with newLine.
// If newLine is empty, it leaves existing content unchanged. If key not found and newLine is non-empty, it appends it within the section.
func upsertInSection(lines []string, section string, keyPrefix string, newLine string) []string {
    if newLine == "" { return lines }
    // Find section range
    start := -1
    end := len(lines)
    for i, l := range lines {
        if strings.TrimSpace(l) == section { start = i; break }
    }
    if start == -1 {
        // append section if missing
        lines = append(lines, "", section)
        start = len(lines) - 1
        end = len(lines)
    } else {
        // find next section
        for i := start + 1; i < len(lines); i++ {
            t := strings.TrimSpace(lines[i])
            if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") { end = i; break }
        }
    }
    // search key within section
    for i := start + 1; i < end; i++ {
        if strings.HasPrefix(strings.TrimSpace(lines[i]), keyPrefix) {
            lines[i] = newLine
            return lines
        }
    }
    // not found -> append before end
    // ensure there's at least one empty line separation
    if end == len(lines) {
        lines = append(lines, newLine)
    } else {
        // insert at end index
        lines = append(lines[:end], append([]string{newLine}, lines[end:]...)...)
    }
    return lines
}

// defaultIfaceFromProcRoute returns the interface name of the default route
func defaultIfaceFromProcRoute() string {
    const path = "/proc/net/route"
    b, err := os.ReadFile(path)
    if err != nil { return "" }
    lines := strings.Split(string(b), "\n")
    for i := 1; i < len(lines); i++ { // skip header
        f := strings.Fields(lines[i])
        if len(f) < 3 { continue }
        iface := f[0]
        dest := f[1]
        if dest == "00000000" { // default route
            return iface
        }
    }
    return ""
}

// maskToPrefix converts dotted netmask to prefix length
func maskToPrefix(mask string) int {
    ip := net.ParseIP(strings.TrimSpace(mask))
    if ip == nil { return 0 }
    ip4 := ip.To4()
    if ip4 == nil { return 0 }
    // Count bits in mask
    m := []byte(ip4)
    var count int
    for _, b := range m {
        for i := 7; i >= 0; i-- {
            if (b>>uint(i))&1 == 1 { count++ } else { break }
        }
    }
    return count
}

// restartNetworkd runs 'systemctl restart systemd-networkd' to apply network changes.
// Requires appropriate permissions (typically root).
func restartNetworkd() error {
    cmd := exec.Command("systemctl", "restart", "systemd-networkd")
    out, err := cmd.CombinedOutput()
    if err != nil {
        log.Printf("systemctl restart systemd-networkd output: %s", string(out))
        return err
    }
    return nil
}

// restartHost attempts to reboot the device. This typically requires root privileges.
// On systems without systemd, it falls back to 'reboot'.
func restartHost() error {
    // Prefer systemd if available
    if _, err := exec.LookPath("systemctl"); err == nil {
        cmd := exec.Command("systemctl", "reboot")
        if out, err := cmd.CombinedOutput(); err != nil {
            log.Printf("systemctl reboot output: %s", string(out))
            return err
        }
        return nil
    }
    // Fallback to classic reboot command
    cmd := exec.Command("reboot")
    if out, err := cmd.CombinedOutput(); err != nil {
        log.Printf("reboot output: %s", string(out))
        return err
    }
    return nil
}

// ensureUniqueID reads /etc/unique_ID if present; if missing, creates it per rules
// Rule: current timestamp -> hex uppercase; prefix with '0'; insert '-' every 4 chars.
func ensureUniqueID() (string, error) {
    const path = "/etc/unique_ID"
    // If exists, read and return
    if b, err := os.ReadFile(path); err == nil {
        id := strings.TrimSpace(string(b))
        if id != "" {
            return id, nil
        }
        // If file exists but empty, fall through to regenerate
    }
    // Generate new ID and write
    id := generateUniqueID()
    if err := os.WriteFile(path, []byte(id), 0o644); err != nil {
        // Return ID even if write fails (permission or other), but report error
        return id, err
    }
    // Also write hostname as "Kan-<ID>" after generating a new ID
    if err := os.WriteFile("/etc/hostname", []byte("Kan-"+id), 0o644); err != nil {
        // Do not fail the operation, just log for visibility
        log.Printf("write /etc/hostname error: %v", err)
    }
    return id, nil
}

func generateUniqueID() string {
    // Use current Unix timestamp in milliseconds
    ts := time.Now().UnixMilli()
    hex := strings.ToUpper(strconv.FormatInt(ts, 16))
    // Prefix first digit with 0
    padded := "0" + hex
    // Insert '-' every 4 characters
    var sb strings.Builder
    for i := 0; i < len(padded); i += 4 {
        end := i + 4
        if end > len(padded) {
            end = len(padded)
        }
        sb.WriteString(padded[i:end])
        if end < len(padded) {
            sb.WriteByte('-')
        }
    }
    return sb.String()
}

func saveConfig(cfg DeviceConfig) error {
    // Store under current working dir
    path := filepath.Join(".", "device_config.json")
    // Merge with existing config if present
    var existing DeviceConfig
    if b, err := os.ReadFile(path); err == nil {
        _ = json.Unmarshal(b, &existing)
    }
    if cfg.ID != "" {
        existing.ID = cfg.ID
    }
    if cfg.IP != "" {
        existing.IP = cfg.IP
    }
    if cfg.Port != "" {
        existing.Port = cfg.Port
    }
    data, err := json.MarshalIndent(existing, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, data, 0o644)
}

// getNetworkParams obtains IP, netmask, gateway and DNS.
// Priority:
// 1) Parse from /etc/system/network/eth*.network (user-specified path)
// 2) Fallback to /etc/systemd/network/eth*.network
// 3) Fallback to live system info: interfaces, /proc/net/route, /etc/resolv.conf
func getNetworkParams() (ip, mask, gw, dns string) {
    // Try systemd network files first (correct path)
    ip, mask, gw, dns = parseNetworkFiles("/etc/systemd/network/eth*.network")
    if ip == "" && mask == "" && gw == "" && dns == "" {
        // Fallback to any .network files in systemd directory
        ip, mask, gw, dns = parseNetworkFiles("/etc/systemd/network/*.network")
    }
    // Fallbacks if any missing
    if ip == "" || mask == "" {
        ip2, mask2 := ipMaskFromInterfaces()
        if ip == "" { ip = ip2 }
        if mask == "" { mask = mask2 }
    }
    if gw == "" { gw = gatewayFromProcRoute() }
    if dns == "" { dns = dnsFromResolvConf() }
    return ip, mask, gw, dns
}

func parseNetworkFiles(glob string) (ip, mask, gw, dns string) {
    matches, _ := filepath.Glob(glob)
    for _, f := range matches {
        b, err := os.ReadFile(f)
        if err != nil { continue }
        lines := strings.Split(string(b), "\n")
        var addrCIDR string
        for _, line := range lines {
            s := strings.TrimSpace(line)
            if s == "" || strings.HasPrefix(s, "#") { continue }
            // Only consider within [Network] section loosely (simple heuristic)
            // We just look for keys regardless of section for simplicity.
            if strings.HasPrefix(s, "Address=") {
                addrCIDR = strings.TrimSpace(strings.TrimPrefix(s, "Address="))
            } else if strings.HasPrefix(s, "Gateway=") {
                gw = strings.TrimSpace(strings.TrimPrefix(s, "Gateway="))
            } else if strings.HasPrefix(s, "DNS=") {
                dnsVal := strings.TrimSpace(strings.TrimPrefix(s, "DNS="))
                // systemd allows multiple space-separated; pick first IPv4
                fields := strings.Fields(dnsVal)
                for _, d := range fields {
                    if isIPv4(d) { dns = d; break }
                }
            }
        }
        if addrCIDR != "" {
            // Expect something like 192.168.1.10/24
            if ipNet := strings.Split(addrCIDR, "/"); len(ipNet) == 2 {
                ipCandidate := strings.TrimSpace(ipNet[0])
                if isIPv4(ipCandidate) {
                    ip = ipCandidate
                    if pfx, err := strconv.Atoi(ipNet[1]); err == nil && pfx >= 0 && pfx <= 32 {
                        mask = prefixToMask(pfx)
                    }
                }
            } else {
                // If no CIDR, try to parse as plain IP
                if isIPv4(addrCIDR) { ip = addrCIDR }
            }
        }
        // If we found anything meaningful, return (prefer first match)
        if ip != "" || mask != "" || gw != "" || dns != "" {
            return ip, mask, gw, dns
        }
    }
    return "", "", "", ""
}

// ipMaskFromInterfaces finds first non-loopback IPv4 addr and netmask
func ipMaskFromInterfaces() (ip, mask string) {
    ifaces, err := net.Interfaces()
    if err != nil { return "", "" }
    // Prefer typical ethernet names
    preferred := func(name string) bool {
        return strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "enp") || strings.HasPrefix(name, "ens") || strings.HasPrefix(name, "eno")
    }
    var firstIP, firstMask string
    for _, iface := range ifaces {
        addrs, _ := iface.Addrs()
        for _, a := range addrs {
            if ipnet, ok := a.(*net.IPNet); ok {
                if ip4 := ipnet.IP.To4(); ip4 != nil {
                    m := netmaskFromIPNet(ipnet)
                    if preferred(iface.Name) {
                        return ip4.String(), m
                    }
                    if firstIP == "" { firstIP = ip4.String(); firstMask = m }
                }
            }
        }
    }
    return firstIP, firstMask
}

func netmaskFromIPNet(n *net.IPNet) string {
    // Convert mask bytes to dotted form
    m := n.Mask
    return net.IP(m).String()
}

// gatewayFromProcRoute parses /proc/net/route to find default gateway (Linux)
func gatewayFromProcRoute() string {
    const path = "/proc/net/route"
    b, err := os.ReadFile(path)
    if err != nil { return "" }
    lines := strings.Split(string(b), "\n")
    for i := 1; i < len(lines); i++ { // skip header
        f := strings.Fields(lines[i])
        if len(f) < 3 { continue }
        dest := f[1]
        gwHex := f[2]
        if dest == "00000000" { // default route
            if ip := hexLEToIPv4(gwHex); ip != "" { return ip }
        }
    }
    return ""
}

func hexLEToIPv4(s string) string {
    if len(s) != 8 { return "" }
    // Little-endian hex; bytes reversed
    b0, _ := strconv.ParseUint(s[6:8], 16, 8)
    b1, _ := strconv.ParseUint(s[4:6], 16, 8)
    b2, _ := strconv.ParseUint(s[2:4], 16, 8)
    b3, _ := strconv.ParseUint(s[0:2], 16, 8)
    return net.IPv4(byte(b0), byte(b1), byte(b2), byte(b3)).String()
}

// dnsFromResolvConf reads first nameserver from /etc/resolv.conf
func dnsFromResolvConf() string {
    const path = "/etc/resolv.conf"
    b, err := os.ReadFile(path)
    if err != nil { return "" }
    lines := strings.Split(string(b), "\n")
    for _, l := range lines {
        s := strings.TrimSpace(l)
        if strings.HasPrefix(s, "nameserver ") {
            ip := strings.TrimSpace(strings.TrimPrefix(s, "nameserver "))
            if isIPv4(ip) { return ip }
        }
    }
    return ""
}

// ifaceName determines a reasonable interface name to report (e.g., eth0).
// Preference order:
// 1) Interface from default route in /proc/net/route
// 2) First interface with IPv4 and typical ethernet prefixes (eth*, enp*, ens*, eno*)
// 3) Any non-loopback interface that has IPv4
func ifaceName() string {
    // 0) Allow manual override via environment variable
    if env := os.Getenv("IFACE_NAME"); strings.TrimSpace(env) != "" { return strings.TrimSpace(env) }
    // 1) Default route on Linux
    if d := defaultIfaceFromProcRoute(); d != "" { return d }
    // 2) Enumerate local interfaces and pick a reasonable one
    ifaces, err := net.Interfaces()
    if err != nil { return "" }
    var fallback string
    for _, iface := range ifaces {
        addrs, _ := iface.Addrs()
        for _, a := range addrs {
            if ipnet, ok := a.(*net.IPNet); ok {
                if ip4 := ipnet.IP.To4(); ip4 != nil {
                    name := iface.Name
                    // Prefer typical ethernet names across Linux and macOS/BSD
                    if strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "enp") || strings.HasPrefix(name, "ens") || strings.HasPrefix(name, "eno") || strings.HasPrefix(name, "en") {
                        return name
                    }
                    if name != "lo" && fallback == "" { fallback = name }
                }
            }
        }
    }
    return fallback
}

func prefixToMask(pfx int) string {
    var m uint32
    if pfx == 0 { return "0.0.0.0" }
    m = ^uint32(0) << (32 - pfx)
    return net.IPv4(byte(m>>24), byte(m>>16), byte(m>>8), byte(m)).String()
}

func isIPv4(s string) bool {
    ip := net.ParseIP(strings.TrimSpace(s))
    return ip != nil && ip.To4() != nil
}