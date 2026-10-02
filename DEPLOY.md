# 部署说明（定制版）

本仓库是 [new-api](https://github.com/QuantumNous/new-api) 的定制分支，改动集中在 Waffo Pancake 支付网关：结算币种改为 CNY、最低充值数量默认 50、手续费转嫁给用户。

镜像由 `.github/workflows/docker-ghcr.yml` 自动构建并发布到 GitHub Container Registry：

```
ghcr.io/ald2015-lr/new-api-custom1:latest      # 跟随最新一次成功构建
ghcr.io/ald2015-lr/new-api-custom1:<短 SHA>     # 锁定某次构建，例如 82a7684
```

仓库 2026-09-27 从 `henfailsf2/new-api-custom` 迁到 `ald2015-lr/new-api-custom1`，镜像地址随之改变，旧地址不再更新。

多架构 `linux/amd64` + `linux/arm64`。包是公开的，拉取不需要登录。若日后把包改成私有，服务器上需要先登录（PAT 权限 `read:packages`）：

```bash
echo "<你的 PAT>" | docker login ghcr.io -u ald2015-lr --password-stdin
```

---

## 一、首次部署

数据库用的是外部 MySQL，容器里只跑应用和 Redis。

```bash
# 1. 拉代码（只需要 deploy 目录里的两个文件，也可以手动 scp 过去）
git clone https://github.com/ald2015-lr/new-api-custom1.git
cd new-api-custom1/deploy

# 2. 配置数据库连接
cp .env.example .env
vi .env          # 填 SQL_DSN，格式 user:password@tcp(host:port)/dbname

# 3. 启动
docker compose pull
docker compose up -d
docker compose logs -f new-api
```

服务监听 `3009`（容器内 `3000`）。日志出现 `server started` 且 `curl -s localhost:3009/api/status` 返回 `"success":true` 即为正常。

`.env` 已被 `.gitignore` 忽略，不会进 Git。仓库里任何文件都不要写真实密码。

---

## 二、从官方镜像平滑切换

现状：跑的是 `calciumion/new-api:latest`，数据在外部 MySQL。切换只是换二进制，数据库不动，所以**不会丢数据**。但顺序不能错——新容器启动时会跑 `AutoMigrate`，那是唯一有风险的动作。

### 1. 先备份数据库（不可跳过）

```bash
mysqldump -h <db-host> -u <user> -p --single-transaction --routines \
  <dbname> > newapi-$(date +%F-%H%M).sql
```

`--single-transaction` 保证 InnoDB 下不锁表。备份文件大小确认一下不是 0 再往下走。

### 2. 记下旧容器的配置

```bash
docker inspect new-api-alpha-us --format '{{json .Config.Env}}' | tr ',' '\n'
docker inspect new-api-alpha-us --format '{{json .Mounts}}' | tr ',' '\n'
```

对照 `deploy/docker-compose.yml`，确认环境变量和你现在跑的一致（尤其是 `SQL_DSN`、`SESSION_SECRET` 如果有的话）。**旧容器的 `./data` 目录要一起带过来**——里面有上传的文件和本地缓存，虽然主数据在 MySQL，但丢了会导致图片/附件 404：

```bash
cp -a /旧目录/data  /新目录/deploy/data
cp -a /旧目录/logs  /新目录/deploy/logs   # 可选
```

### 3. 停旧容器，起新容器

两个容器都占 3009，必须先停后起：

```bash
docker stop new-api-alpha-us            # 只 stop，不要 rm，留着回滚
cd /新目录/deploy
docker compose up -d
docker compose logs -f new-api
```

日志里会有 `AutoMigrate` 相关输出。看到服务正常监听后，立即验证：

- 后台能登录，用户/渠道/日志数据都在
- 发一次 API 请求，确认计费正常扣费
- 支付页面显示的最低充值数量是 **50**
- 用 50 元档试算，用户实付应该显示 **55.78**

确认无误后再清理旧容器：

```bash
docker rm new-api-alpha-us              # 确认新版跑稳了再执行
```

### 4. 切换后要做的配置检查

`WaffoPancakeMinTopUp`、三个手续费配置项都是数据库 `options` 表里的持久化配置。你的库里原本没有这几行，所以第一次启动会用代码默认值（50 / 转嫁开启 / 0.039 / 3.6）。想改的话去**后台 → 系统设置 → 支付网关 → Waffo Pancake → 计价与手续费**，改完立即生效，不用重启。

注意：**一旦在后台点过保存，这些值就写进数据库了，此后数据库优先于代码默认值**。以后再调只能走后台，改代码重新构建不会生效。查当前实际值：

```sql
SELECT `key`, `value` FROM options WHERE `key` LIKE 'WaffoPancake%';
```

（`key` 是 MySQL 保留字，反引号不能省。）

---

## 三、合并上游更新并重新构建

当前基线：`v1.0.0-rc.41`（2026-10-01 从 rc.40 合并上来；rc.40 是 2026-09-27 从 rc.30 合并的）。上游发新版时：

```bash
git remote add upstream https://github.com/QuantumNous/new-api.git   # 只需一次
git fetch upstream --tags

git checkout main
git merge v1.0.0-rc.42        # 换成上游最新的 release tag，不要用 upstream/main
```

冲突大概率出现在这几个文件（就是定制改动所在的位置）：

| 文件 | 定制内容 |
|---|---|
| `service/waffo_pancake.go` | 三处 `USD` → `CNY` |
| `setting/payment_waffo_pancake.go` | `MinTopUp = 50`，三个手续费变量 |
| `controller/topup_waffo_pancake.go` | `getWaffoPancakePayMoney` 里的反推逻辑 |
| `model/option.go` | 三个手续费键、`LogResponseModelEnabled` 的注册与解析 |
| `web/src/features/system-settings/integrations/*` | 「计价与手续费」UI |
| `web/src/i18n/locales/*.json` | 16 条新文案（注意保持 `footer.new\u0061pi…` 那个键的转义形式不变，别用 JSON 库整体重写这些文件） |
| `router/web-router.go`、`middleware/cache.go`、`middleware/rate-limit.go` | 静态资源不限流、缺失 chunk 返回 404、immutable 缓存（见第六节） |
| `service/relay_error.go`（rc.40 前在 `controller/relay.go`） | `DecideRelayRetry` 开头的"已向客户端输出就不再重试"守卫 |
| `controller/relay.go` | defer 里流已开始时改走 `helper.WriteStreamError` |
| `relay/helper/stream_scanner.go`、`stream_error.go`、`common.go` | 裸 `[DONE]`、流内错误事件、写超时续期 |
| `relay/channel/openai/relay-openai.go` | `thinking_to_content` 交错思考 |
| `relaykit/.../gemini_chat/to_oai_chat_resp.go` | thought 与正文分字段 |
| `common/constants.go`、`model/log_other.go`、`web/src/features/system-settings/maintenance/log-settings-section.tsx` | 「在使用日志中显示响应模型」开关（默认关；关闭时日志接口不返回 `response_model`，黄色「响应模型」标记对所有人隐藏） |
| `web/src/lib/{chunk-load-error,stale-bundle}.ts`、`features/errors/general-error.tsx`、`i18n/config.ts`、`main.tsx`、`lib/http-client.ts`、`rsbuild.config.ts` | 前端自愈刷新、新版本提示、语言包懒加载（见第六节） |

rc.30 → rc.40 这次合并的经验：上游把重试判断从 `controller/relay.go` 挪到了 `service/relay_error.go`，重构了 `web/src/lib/http-client.ts` 和登录跳转 hook，`main.tsx` 里 `i18next` 的导入被上游删掉了（我们的 `ensureLocale(i18next.language)` 还要用，合并后要补回来）。解冲突时以上游为主体，把上表里的定制点重新加回去。

rc.40 → rc.41 是无冲突合并，上表定制点全部原样保留。

解冲突的原则：**保留定制改动，接受上游其他部分**。解完必须验证：

```bash
go build ./...
gofmt -l . | grep -v '^web/'
go test ./controller/ -run WaffoPancake        # 手续费换算的断言在这里
cd web && bun install && bun run typecheck && bunx vitest run
```

`.github/workflows/docker-ghcr.yml` 和 `deploy/` 都放在上游没有的路径上，合并时不会冲突。

推上去后自动触发构建：

```bash
git push origin main
```

在 [Actions 页面](https://github.com/ald2015-lr/new-api-custom1/actions) 等构建完成（约 7 分钟），然后在服务器上更新：

```bash
cd deploy
docker compose pull
docker compose up -d
```

上游如果动了数据库 schema，更新前照样先 `mysqldump`。

---

## 四、回滚

### 回滚到上一个定制版本

镜像 tag 是 commit 短 SHA，改 `.env` 一行即可：

```bash
cd deploy
sed -i 's/^NEW_API_TAG=.*/NEW_API_TAG=<上一个短 SHA>/' .env
docker compose up -d
```

历史 tag 在 [Packages 页面](https://github.com/users/ald2015-lr/packages/container/package/new-api-custom1) 能查到。

### 回滚到官方镜像

```bash
cd deploy
docker compose down
docker run -d --name new-api-alpha-us --restart always \
  -p 3009:3000 \
  -v $(pwd)/data:/data -v $(pwd)/logs:/app/logs \
  -e SQL_DSN='<你的 DSN>' \
  -e REDIS_CONN_STRING=redis://redis \
  -e TZ=Asia/Shanghai \
  calciumion/new-api:latest --log-dir /app/logs
```

或者直接把 `docker-compose.yml` 里的 `image` 改回 `calciumion/new-api:latest` 再 `docker compose up -d`（Redis 服务还在 compose 里，网络会自动接上）。

回滚的两个注意点：

1. **定制功能会全部失效**——官方镜像结算币种是 USD、最低充值数量读数据库（你后台设过就是那个值）、没有手续费转嫁。已经产生的订单和用户余额不受影响。
2. **数据库 schema 不会自动回退**。当前定制版基于 `v1.0.0-rc.41`。rc.41 启动时新建 `user_access_tokens` 表，并在 `options` 表写入一行 `LegacyAccessTokenRetireAt`（见第五节）；rc.40 启动时会做一次 `options` 表主键修复、新建审计日志/账号安全/请求策略等多张表并给若干列改类型；回滚到 rc.40 之前的镜像时这些新表和列会留在库里——通常是加法式变更，旧版本能正常跑；万一起不来，用切换前的 `mysqldump` 恢复。

---

## 五、故障排查

| 现象 | 排查方向 |
|---|---|
| `docker pull` 报 `denied` | GHCR 包被设成私有了，去 Packages 设置改回 Public，或按开头的命令 `docker login` |
| 容器起来就退出 | `docker compose logs new-api`，八成是 `SQL_DSN` 写错或 MySQL 不通 |
| 最低充值数量还是 1 | 数据库 `options` 表里有旧行，用上面那条 SQL 查，`DELETE` 掉再重启，或直接在后台改 |
| 支付金额不对 | 后台看「计价与手续费」四个值；反推公式是 `实付 = (标价 + 固定费) / (1 - 费率)`，50 元档应为 55.78 |
| 前端页面白屏 | 强刷清缓存；仍然白屏看 `docker compose logs`，可能是构建时前端产物没打进去 |
| 用户看到大大的「500」错误页，刷新就好 | 见下一节。那个 500 是前端兜底页的固定文案，不代表后端返回了 500 |
| 升级 rc.41 约 30 天后，调管理接口的脚本报 `legacy access token has been retired` | rc.41 引入新的个人访问令牌（`nap_` 开头，后台左侧「安全」页的「访问令牌」里创建），旧的「系统访问令牌」从 rc.41 第一次启动起 30 天后停用，截止时间记在 `options` 表 `LegacyAccessTokenRetireAt`（服务端管理，后台改不了）。到「安全」→「访问令牌」新建一个换上即可。**只影响调 `/api/...` 管理接口的令牌；用户调模型用的 `sk-` 令牌完全不受影响** |

---

## 六、「500」错误页与首屏慢：定制版做了什么

前端错误页（`web/src/features/errors/general-error.tsx`）对任何没有 HTTP 状态码的错误都打印固定的 `500`。上游有三条路径会走到这里，定制版逐条处理了：

| 根因 | 上游行为 | 定制版行为 |
|---|---|---|
| 静态资源被每 IP 限流（默认 120 次 / 180 秒，JS/CSS 也计数；一次冷加载就要十几个请求；CDN/反代未配 `TRUSTED_PROXIES` 时全站共用一个桶） | 路由 chunk 收到 429 → `ChunkLoadError` → 500 页 | 限流移到静态资源之后，只对页面 HTML 兜底和 404 计数 |
| 发布新版后，旧标签页请求已不存在的 chunk | 返回 `200 text/html` 的 index.html，浏览器把 HTML 当 JS 执行 → 500 页 | 缺失的 `/static/*` 返回 `404 + no-store`；前端识别 chunk 加载失败后**自动整页刷新一次**（sessionStorage 防死循环），刷新失败才显示「页面部分资源加载失败，请刷新」和一个刷新按钮 |
| Redis 抖动 | 限流中间件直接回 500，每个资源请求都变 500 | 页面层限流在 Redis 出错时放行并记日志；API 层限流保持原样（拒绝） |

首屏慢的处理：

- `/static/*` 文件名带内容 hash，现在按 `public, max-age=31536000, immutable` 缓存，并带 ETag，回源验证得到 304 而不是重新下载
- 七种语言包原本全部打进首屏同步 chunk（约 2.5 MB 原始 / 770 KB gzip），现在只内置英文，当前语言按需加载
- 登录态刷新请求加了 30 秒超时，卡住时以临时错误结束而不是让首屏一直等（普通 API 客户端不加，渠道测试等长请求不受影响）
- 每个响应都带 `X-New-Api-Version`；前端发现与自身构建版本不一致时弹一次「有新版本，请刷新」提示，避免用户在旧页面里一路点到已失效的 chunk

**发布后建议做的两件事：**

1. 如果 `:3009` 前面有 CDN 或 nginx，且它们不在同一台机器/内网，在 compose 里填 `TRUSTED_PROXIES`（回源网段），否则后端看到的客户端 IP 全是代理 IP。看容器启动日志有没有 `TRUSTED_PROXIES` 相关警告。
2. 确认线上状态：

```bash
# 静态资源：应为 200，Cache-Control 带 immutable
curl -sI https://<你的域名>/static/js/index.$(hash).js | grep -iE "^(HTTP|cache-control|etag)"
# 不存在的 chunk：应为 404，不是 200 text/html
curl -sI https://<你的域名>/static/js/async/nope.js | grep -iE "^(HTTP|cache-control|content-type)"
# 连打 150 次静态资源不应出现 429
for i in $(seq 1 150); do curl -s -o /dev/null -w "%{http_code}\n" https://<你的域名>/static/js/index.$(hash).js; done | sort | uniq -c
```

`$(hash)` 换成 `curl -s https://<你的域名>/ | grep -o 'static/js/index[^"]*'` 查到的实际文件名。

---

## 七、流式回复「生成一半报错」「思考链—正文反复」：定制版做了什么

排查结论：两种现象都能由网关自身触发，**不是（或不只是）模型问题**。

| 现象 | 根因（上游代码） | 定制版行为 |
|---|---|---|
| 思考链 → 正文 → 思考链 → 正文 … | 上游渠道在流传输中途返回错误事件（Claude 的 `overloaded_error` 等）时，网关按 500 走**换渠道重试**，但没有检查"已经给客户端发过内容"，于是把第二份完整回答（从思考链开始）**拼接到同一个 HTTP 响应里**，重试几次就重复几次 | 只要已向客户端写出过字节，一律不再重试 |
| 回复中途报错 | 重试耗尽或不可重试时，网关把 JSON 错误体 `c.JSON(...)` **直接拼到已经开始的 SSE 流后面**，客户端 SDK 解析失败 | 改为按协议发送流内错误事件：OpenAI 格式 `data: {"error":…}` + `[DONE]`，Claude 格式 `event: error`，客户端能正确显示错误信息 |
| Gemini 渠道思考和正文混在一起、正文跑进思考框 | Gemini 同一个 chunk 里同时有 thought 和答案时，整个 chunk 被标成 `reasoning_content` | thought 进 `reasoning_content`，答案进 `content` |
| 上游以裸 `[DONE]` 结束时报错 | 解析器把裸 `[DONE]` 切成 `]` 交给适配器 | 正确识别为结束 |
| 生成很久后最后一个 chunk 发不出 | 30 秒写超时只在循环内续期，收尾写入可能撞上过期的超时 | 每次写入都续期 |
| 开了「思维链转内容」时思考文字混进正文 | 第一个思考 chunk 若同时带正文会丢正文；正文之后再出现思考不会重新加 `<think>` | 都处理了，交错思考也能正确包 `<think>` |

**上线后怎么确认修好了：**

```bash
# 以前出问题的请求会有这一行；修复后，已开始输出的流不会再出现它
docker compose logs new-api 2>&1 | grep '重试：'
# 流中途出错时的记录（配合 request id 看 received= 是否大于 0）
docker compose logs new-api 2>&1 | grep -E 'relay error:|stream ended: reason='
```

如果 `重试：` 仍然频繁出现（针对还没输出内容的请求，这是正常重试），说明某个渠道经常在建连阶段就失败，去后台看渠道的错误日志。

**仍然属于上游/模型行为、网关不负责的：** Claude 4 的交错思考（thinking → text → thinking 是模型真实输出）；模型自身的复读/死循环；上游返回 `finish_reason: length`（`max_tokens` 不够）。这类情况修复后仍会原样透传。

