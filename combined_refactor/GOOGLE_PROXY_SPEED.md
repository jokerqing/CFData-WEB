# Google 文件真实代理测速

此分支将 Google 文件下载集成到 CFData 原有测速和定时任务中。启用后，下载流量通过候选节点的 VLESS + TLS + WebSocket 代理，不再使用 Cloudflare 文件进行下载测速。TCP 和 WebSocket 探测仍用于检查候选节点的连通性。

## 部署

在 `combined_refactor/` 中构建 Linux amd64 可执行文件：

```sh
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o ../release_assets/cfdata-test .
```

将以下文件放在可执行文件所在目录，确保运行用户能够读取配置、执行 Xray，并写入测速结果：

```text
cfdata
cfdata-proxy-speed.json
proxy-speed/xray
proxy-speed/ca-certificates.crt
```

`xray` 使用与部署平台匹配的静态可执行文件，已验证版本为 26.3.27。证书文件可使用仓库内的 `ca-certificates.crt`；scratch 容器尤其需要此文件，TLS 证书校验保持开启。

在服务器上创建私密配置 `cfdata-proxy-speed.json`：

```json
{
  "enabled": true,
  "uri": "vless://YOUR_UUID@proxy.example:443?security=tls&type=ws&sni=worker.example&host=worker.example&path=%2Fproxyip%3Drelay.example%3A443&fp=chrome&alpn=http%2F1.1"
}
```

将示例替换为实际生产代理参数，并限制文件读取权限。测速时仅将 URI 中的连接地址和端口替换为候选节点，保留 UUID、SNI、Host、WebSocket 路径和 ProxyIP 等传输参数。配置通过标准输入传给 Xray，凭据不写入日志。真实配置、令牌和运行文件均不应提交到 GitHub。

配置缺失或 `enabled` 为 `false` 时使用原有测速方式；配置不可读或格式错误时，代理测速返回错误。

## 测速规则与定时设置

- 下载地址固定为 `https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb`。
- 每个节点连续下载三轮，每轮请求前 10,000,000 字节；必须收到 HTTP 206、正确的 Content-Range 和完整字节数。
- 每轮超时 15 秒，三轮均须达到 20 Mbps。返回三轮中的最低速度，计时包含连接与下载过程。
- 非标模式按公网 IPv4 去重，同一 IP 的不同端口只保留一个候选。
- 建议定时任务设置 `mode: "nsb"`、`speedTest: 1`、`speedMin: 2.5`、`speedLimit: 10`，并显式填写上述 `speedURL`。`speedMin` 按 MiB/s 计算，2.5 MiB/s 约为 20.97 Mbps。
- `sourceURLs` 可填写 bestcf.pages.dev 的多个候选地址列表。代理测速模式下单个来源临时失败会跳过，所有来源失败则任务失败。
- 每 15 分钟执行意味着在 `times` 中配置每天各小时的 `HH:00`、`HH:15`、`HH:30`、`HH:45`（Asia/Shanghai）。已有任务仍在运行时不会启动重叠批次。

## 发布给 HAProxy

非标定时任务完成筛选后，将本轮合格节点和三轮下载证据原子写入 `cfdata-proxy-quality.json`（schema 3）。仅当合格节点数达到 `max(2, speedLimit)` 时替换该文件；十节点配置下，不足十个会报错并保留上次发布结果。

当前部署在 192.168.88.19 执行全部下载测速，清单中的 `sourceIP` 为此部署地址。192.168.88.18 的 HAProxy 通过另外部署的同步程序消费清单；本分支提供清单生产端，不包含 HAProxy 的部署脚本。
