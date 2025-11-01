# 设备发现与配置

## 概述
- 主机监听程序（Linux/ARM）：监听 UDP `60000`，收到 `TF` 返回 `TF|ID=<id>|PORT=<port>`；收到 `CFG|...` 解析配置并保存到 `device_config.json`。
- Windows 客户端（GUI）：广播发送 `TF` 扫描设备，列出 `IP/端口/ID`；可发送 `CFG|ID=..|IP=..|PORT=..` 给选中设备或以广播模式发送。

## 构建

### Linux ARM（主机监听程序）
```
GOOS=linux GOARCH=arm GOARM=7 go build -o bin/udp-server ./main.go
```
（如为 aarch64/ARM64：`GOARCH=arm64`）

运行：
```
DEVICE_ID=HOST-ABC UDP_PORT=60000 ./bin/udp-server
```

### Windows（GUI 客户端）
依赖：Fyne（`fyne.io/fyne/v2`）。首次构建会自动拉取依赖。

macOS 上交叉编译 Windows：
```
GOOS=windows GOARCH=amd64 go build -o bin/discover_gui.exe ./cmd/discover_gui
```

本机运行（macOS/Linux 也可运行 GUI 以测试）：
```
go run ./cmd/discover_gui
```

## 使用
- 启动服务器后，在同一网段运行 GUI，点击“扫描设备(发送TF)”即可在列表中看到设备。
- 选中设备后，填写需要修改的 `ID/IP/PORT`，点击“发送配置(CFG)”即可下发。
- 勾选“广播配置模式”则对整个网段广播配置（谨慎使用）。

## 协议说明
- 发现请求：`TF`
- 发现响应：`TF|ID=<id>|PORT=<port>`
- 配置下发：`CFG|ID=<id>|IP=<ip>|PORT=<port>`（可选字段不填则不修改）

## 注意事项
- 服务器当前将配置持久化到 `device_config.json`，未直接修改系统网络设置（避免权限与系统差异问题）。如需生效到系统网络，请按目标设备的发行版编写相应脚本并以适当权限执行。
- 广播包在部分网络环境可能受限；如发现不到设备，可尝试直连发送到设备IP。