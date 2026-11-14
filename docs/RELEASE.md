# 云端构建与发行

仓库：https://github.com/LE-saber/Meshport

## 验证源码

推送到 `main` 或提交 Pull Request 会运行 **CI**：

- 在 Ubuntu 上运行 Go 格式、静态检查和客户端 JavaScript 语法检查。
- 构建真实 frps/frpc，执行带 race detector 的单元和集成测试。
- 交叉构建 Linux、Windows 的 amd64 和 arm64 桌面包。

服务端使用 Linux 系统接口，完整的 `go test ./...` 应在 Linux 上执行。

## 构建草稿发行版

1. 进入仓库 **Actions → Release → Run workflow**。
2. 选择 `main`，可填写版本（例如 `0.1.0-rc.1`，不带 `v`）。留空时使用 `0.0.0-dev.<运行编号>`。
3. 等待 server、desktop、android 和 publish 全部成功。
4. 在 Actions 的 Artifacts 或 Releases 的草稿中下载产物并进行实机验证。

手动运行生成 Draft Release，不会立即公开发布。

| 产物 | 平台 |
|---|---|
| `meshport-server-<版本>-linux-<架构>.tar.gz` | Linux amd64 / arm64 |
| `meshport-desktop-<版本>-linux-<架构>.tar.gz` | Linux amd64 / arm64 |
| `meshport-desktop-<版本>-windows-<架构>.zip` | Windows amd64 / arm64 |
| `meshport-android-<版本>-<ABI>.apk` | universal / arm64-v8a / armeabi-v7a / x86_64 |
| `SHA256SUMS.txt` | 发行文件校验和 |

构建版本来自 `go.mod`；Android 构建同时使用工作流指定的 Java 和 Gradle 版本。

Android 的网络接口依赖 `anet` 使用 Go 内部符号，因此 Go 1.23 及以上需要 `-ldflags "-checklinkname=0"`。工作流和 `make android-aar` 已包含该参数，依据 [anet 上游构建说明](https://github.com/wlynxg/anet#how-to-build-with-go-1230-or-later)。这个兼容参数仅用于 Android；升级 Go 或该依赖后仍需验证实机网络行为。

## Android 正式签名

在仓库 **Settings → Secrets and variables → Actions** 配置：

- `ANDROID_KEYSTORE_BASE64`
- `ANDROID_KEYSTORE_PASSWORD`
- `ANDROID_KEY_ALIAS`
- `ANDROID_KEY_PASSWORD`

没有配置时，现有工作流使用临时 debug 签名，适合试装；不同构建之间可能无法覆盖升级。正式发行前应配置并妥善保管固定签名密钥。不要把密钥或密码提交到 Git。

## 正式发布

确认 CI 和草稿构建成功，并完成桌面、Android、跨 NAT 和 TURN 路径的实机验证后，再发布草稿。

也可以向已经验证的提交推送 `v<版本>` 标签；标签会触发 Release 工作流并直接公开发布。构建测试阶段优先使用手动草稿流程。

管理控制台前端目前仍是设计 Demo；构建成功不代表控制台已经完成 API 对接，详见项目 README。
