# sb-manager-bot

`sb-manager-bot` 是一个面向多用户的烧饼论坛（sb.sb）通知 Telegram Bot。每位 Telegram 用户可绑定一个论坛账号；程序定期读取通知页，并把新通知发回该用户与 Bot 的私聊。

> Cookie 等同于论坛登录权限。程序会用 AES-256-GCM 加密后写入 SQLite，但自托管管理员仍应保护数据库、环境变量、备份和日志。`SBM1` 只是 Base64URL 封装，不是加密。

## 快速部署

必填环境变量：

- `TELEGRAM_BOT_TOKEN`：从 BotFather 获取。
- `CREDENTIAL_ENCRYPTION_KEY`：32 个随机字节的 Base64。Linux 可用 `openssl rand -base64 32` 生成；一旦丢失，已有 Cookie 无法恢复。
- `ADMIN_TELEGRAM_IDS`：管理员 Telegram 数字 ID，多个用逗号或空格分隔。

可选变量：

- `DATABASE_FILE=/data/sb-manager.db`
- `POLL_INTERVAL=60s`
- `FORUM_REQUEST_RATE=3`
- `POLL_WORKERS=8`
- `MAX_USERS=500`
- `HTTP_ADDR=:8080`

复制 `.env.example` 为 `.env`，填写密钥后运行：

```sh
docker compose up -d
docker compose logs -f
```

SQLite 必须位于持久卷中，并且服务只能运行一个副本。Railway 部署时把卷挂载到 `/data`，设置上述变量，并确保不启用多副本；平台会使用 `/healthz` 和 `/readyz` 检查服务。500 名活跃用户在全局 3 次 GET/秒的限制下，实际轮询周期可能约 3 分钟。

镜像支持 `linux/amd64` 和 `linux/arm64`：

```sh
docker pull ghcr.io/krabdo/sb-manager-bot:latest
```

## 用户流程

1. 给 Bot 发送 `/bind`。
2. 安装 [Tampermonkey 用户脚本](https://raw.githubusercontent.com/krabdo/sb-manager-bot/main/userscript/sb-manager-credentials.user.js)。
3. 打开自己的 `https://sb.sb/u/<UID>/?tab=notifications`，点击“复制 Bot 凭据”。
4. 立即把 `SBM1...` 粘贴到 Bot 私聊。Bot 必须先成功删除该消息，才会验证并保存绑定。
5. 绑定成功后清除剪贴板历史。

Tampermonkey Beta 目前可通过 `GM.cookie`/`GM_cookie` 读取 HttpOnly Cookie。稳定版若无法读取，脚本会在本页弹窗中指导用户从浏览器 Network 面板复制完整 `Cookie` 请求头值。脚本不声明 `@connect`、不发网络请求、不记录 Cookie。凭据不会放入 Telegram deep link。

首次绑定只发送当前最新一条，其余第一页通知作为基线。同一个论坛 UID 更新 Cookie 时保留去重历史；更换 UID 时会清空旧历史并建立新基线。

## 命令

- `/start`、`/help`：帮助与风险说明
- `/bind`：绑定步骤
- `/status`：论坛 UID、状态和上次成功时间（永不显示 Cookie）
- `/pause`、`/resume`：暂停或恢复自己的轮询
- `/unbind`：二次确认后删除 Cookie 和全部去重记录

管理员：

- `/admin_stats`
- `/admin_ban <telegram_id>`、`/admin_unban <telegram_id>`
- `/admin_pause`、`/admin_resume`

## 运行机制与故障排查

- 8 个有界 worker 按最早到期时间公平调度；全部论坛分页请求共享 3 RPS 限制。
- 网络错误采用 1、2、4、8、15 分钟退避；HTTP 429 遵守 `Retry-After` 并触发共享冷却。
- Cookie 失效后用户变为 `rebind_required`，停止轮询且只告警一次。
- 页面结构变化会触发全局熔断并通知管理员，不更新任何用户的去重状态。
- Telegram 发送成功后才记录 seen；每用户最多保留最近 2048 条。
- 日志只使用 Telegram 数字 ID 定位问题，不记录 Cookie、`SBM1`、用户名或通知正文。

`/healthz` 只表示进程存活；`/readyz` 还会检查数据库可写、Bot 已初始化和调度器正在运行。数据库损坏、密钥错误或已存 Cookie 无法解密时，程序会拒绝启动。升级前应停止服务并备份 `/data`，然后拉取新镜像并重新启动；不要在运行时复制 SQLite 主文件而忽略 WAL。

## 本地开发

需要 Go 1.26 和 Node.js：

```sh
go test -race ./...
go vet ./...
node --test userscript/*.test.js
```

本项目采用 Apache-2.0 许可证。第三方依赖归属见 `THIRD_PARTY_NOTICES.md`。
