# mihomo-exporter
Exporter for mihomo metrics

## 安装以及使用

如果你对Prometheus和`mihomo_exporter`不熟悉，可以参考[简单的分步指南](https://prometheus.io/docs/guides/node-exporter/)。

默认情况下，`mihomo_exporter`在HTTP端口9988上进行监听。有关更多选项，请查看`--help`输出。

支持两种方式使用docker或cli

### Docker compose

目前没有发布到docker hub的打算，直接在本地编译吧
```
services:
  mihomo-exporter:
    build: .
    restart: unless-stopped
    environment:
      host: 你的mihomo地址：端口
      secret: 你的mihomo密钥
    ports:
      - "9988:9988"
```
### cli
  -host string
        mihomo host (e.g. 127.0.0.1:9090)
  -listen string
        listen addr
  -secret string
        mihomo secret

举例
mihomo-exporter-windows-amd64.exe --host 127.0.0.1:9090 --secret 123456

## 指标

总上行|mihomo_upload_bytes_total
总下行|mihomo_download_bytes_total
实时上行|mihomo_upload_bps
实时下行|mihomo_download_bps
Exporter 状态|mihomo_exporter_up
内存用量|mihomo_memory_usage_bytes
当前连接数|mihomo_active_connections
当前运行版本|mihomo_version_info
节点延迟|mihomo_proxy_delay_ms
节点存活|mihomo_proxy_alive
节点选择|mihomo_proxy_selected