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

当前基线：`v1.0.0-rc.42` + 上游 `main` 到 `7aa3531ef` 的 1 个修复提交（2026-10-08 合并；当时没有比 rc.42 更新的正式版）。rc.42 之前依次是：rc.41（2026-10-01，从 rc.40 合并）、rc.40（2026-09-27，从 rc.30 合并）。上游发新版时：

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
| `web/src/i18n/locales/*.json` | 46 条新文案（注意保持 `footer.new\u0061pi…` 那个键的转义形式不变，别用 JSON 库整体重写这些文件） |
| `router/web-router.go`、`middleware/cache.go`、`middleware/rate-limit.go`、`middleware/frontend_static.go` | 静态资源不计入页面限流、缺失 chunk 返回 404、immutable 缓存与 304、页面/静态限流在 Redis 故障时放行（见第六节） |
| `service/relay_error.go`（rc.40 前在 `controller/relay.go`） | `DecideRelayRetry` 开头的"已向客户端输出就不再重试"守卫 |
| `controller/relay.go` | defer 里流已开始时改走 `helper.WriteStreamError` |
| `relay/helper/stream_scanner.go`、`stream_error.go`、`common.go` | 裸 `[DONE]`、流内错误事件、写超时续期 |
| `relay/channel/openai/relay-openai.go` | `thinking_to_content` 交错思考 |
| `relaykit/.../gemini_chat/to_oai_chat_resp.go` | thought 与正文分字段 |
| `common/constants.go`、`model/log_other.go`、`web/src/features/system-settings/maintenance/log-settings-section.tsx` | 「在使用日志中显示响应模型」开关（默认关；关闭时日志接口不返回 `response_model`，黄色「响应模型」标记对所有人隐藏） |
| `model/subscription.go`、`service/{billing_session,funding_source,quota,task_billing}.go`、`relay/common/relay_info.go`、`service/log_info_generate.go` | 结算超出订阅剩余额度时，订阅扣满、超出部分按套餐设置转扣钱包（见第九节）；订阅优先回退钱包：`user_subscriptions` 只按列更新（不再整行 `Save`），「是否允许回退钱包」看套餐当前设置而不是购买时的快照（见第九节） |
| 新文件 `setting/operation_setting/group_access_setting.go`、`model/group_access.go`、`service/group_access.go`、`middleware/group_access.go`，以及 `middleware/auth.go`、`middleware/distributor.go`、`service/group.go`、`controller/{group,pricing,token,option}.go`、`model/{option,user_cache}.go`、`constant/context_key.go`、`i18n/` 里的小钩子 | 分组充值门槛 + 白名单（见第八节） |
| `web/src/features/system-settings/billing/group-access-*`、`features/keys/*`、`features/pricing/*` | 分组充值门槛的后台设置页、令牌分组锁定显示、模型广场门槛标记 |
| `web/src/components/layout/components/public-header.tsx` | 手机端首页顶栏直接显示「模型广场」和公告铃铛；平板横屏后不再锁死页面滚动 |
| `model/topup.go` | 管理员补单：Creem 订单按额度原样入账（上游会放大 50 万倍）；重复补单不再给用户 0 记日志 |
| `middleware/task_plugin.go`、`relay/relay_task.go`、`relay/mjproxy_handler.go` | 基于旧任务的续作/放大请求也受分组准入限制 |
| 新文件 `model/channel_quota_limit.go`、`controller/channel_quota_limit.go`，以及 `model/{main,channel,channel_cache,ability}.go`、`service/channel_select.go`、`controller/relay.go`、`router/channel-router.go`、`i18n/` 里的小钩子；前端 `web/src/features/channels/*` | 渠道限额（见第十节）；新增 `channel_quota_limits` 表 |
| 新文件 `setting/operation_setting/group_billing_setting.go`、`service/free_group.go`，以及 `service/{billing,quota,task_billing}.go`、`controller/pricing.go`、`model/option.go` 里的小钩子；前端 `features/system-settings/billing`、`features/pricing`、`features/usage-logs` | 不扣费（免费）分组（见第十一节） |
| `model/model_pricing_conversion.go`（`ConvertAllModelPricing`）、`controller/model_pricing_config.go`、`router/api-router.go`、`pkg/billingexpr/fixed.go`、`model/pricing.go`；前端 `features/model-pricing`、`features/system-settings/models`、`features/pricing` | 一键把旧定价全部转为计费表达式；修复编辑器把占位表达式 `p * 0 + c * 0` 存成 $0 计费；按次表达式在模型广场归入「按次计费」（见第十二节） |
| 新文件 `relay/helper/keepalive_writer.go`，以及 `controller/relay.go`（安装保活写出器、出错时按已提交的格式写错误）、`relay/channel/api_request.go` 与 `relay/helper/stream_scanner.go`（装了写出器时跳过旧的 ping）、`setting/operation_setting/general_setting.go`、`model/option.go`、`controller/plugin_protocol{,_image}.go`（插件同步任务）；前端 `features/system-settings/models/global-settings-card.tsx` | 长请求保活：流式按客户端协议发心跳（Claude 发 `event: ping`，Responses 在第一个事件之后发 `event: keepalive`，其余发 `: PING`）、非流式发前导换行，只发过心跳时仍可重试（见第十三节） |
| `relay/channel/api_request.go`（`DoApiRequest` / `DoFormRequest` 用 `http.NewRequestWithContext`）、`service/relay_error.go`（`ClientGone`，`DecideRelayRetry` 的 `client_gone`）、`service/request_policy.go`、`controller/relay.go`（客户端已断开时不调用 `processChannelError`）、`relay/helper/stream_scanner.go`（读上游出错时先看客户端是否已断开）、`relay/responses_websocket.go`（WebSocket 连接上游失败时同样跳过 `ProcessChannelError`）、`relay/channel/{ollama/stream.go,coze/relay-coze.go,zhipu/relay-zhipu.go,cloudflare/relay_cloudflare.go,openai/audio.go}`（自己读上游的处理器：客户端断开后按已收到的文本结算、不记 ERR，TTS 非流式退款）；前端 `features/system-settings/request-policies/policy-label.ts` | 客户端断开时取消上游请求，不重试、不算渠道错误、退还预扣额度（见第十三节） |
| 新文件 `setting/operation_setting/model_square_setting.go`，以及 `controller/pricing.go`（`model_square` 字段）、`model/option.go`；前端 `features/pricing/lib/model-variants.ts`、`model-family*.tsx`、`model-variant-list.tsx`、`pricing-family-cells.tsx`、`features/system-settings/models/model-square-*` | 模型广场按基础模型合并变体（见第十四节） |
| `web/src/features/home/components/sections/hero.tsx` | 首页按钮：登录后显示「前往仪表盘 / 模型广场 / 文档」 |
| `web/src/lib/{chunk-load-error,stale-bundle}.ts`、`features/errors/general-error.tsx`、`i18n/config.ts`、`main.tsx`、`lib/http-client.ts`、`rsbuild.config.ts` | 前端自愈刷新、新版本提示、语言包懒加载（见第六节） |

rc.30 → rc.40 这次合并的经验：上游把重试判断从 `controller/relay.go` 挪到了 `service/relay_error.go`，重构了 `web/src/lib/http-client.ts` 和登录跳转 hook，`main.tsx` 里 `i18next` 的导入被上游删掉了（我们的 `ensureLocale(i18next.language)` 还要用，合并后要补回来）。解冲突时以上游为主体，把上表里的定制点重新加回去。

rc.40 → rc.41、rc.41 → upstream main（1a4166d8e）都是无冲突合并，上表定制点全部原样保留。

rc.41 → rc.42（2026-10-08）：上游自己也修了「前端静态文件把页面限流用光」——静态文件改为预压缩直出，并单独加了一个默认关闭的静态限流（`GLOBAL_STATIC_RATE_LIMIT_ENABLE` / `GLOBAL_STATIC_RATE_LIMIT` / `GLOBAL_STATIC_RATE_LIMIT_DURATION`）。冲突在 `router/web-router.go`、`middleware/rate-limit.go`：以上游结构为主，保留我们的缺失 chunk 404、Redis 故障放行，并在预压缩路径补上 `If-None-Match` → 304（上游这条路径不回 304，会让浏览器每次重新下载整个 chunk）。另外上游把 Go 升到 1.26、构建时用了 moejs 的 PGO 文件，Dockerfile 已同步，不需要手动处理。

注意 rc.41 → upstream main（1a4166d8e）那次合并的 7 个提交里有一条（feefe09f2）：Waffo Pancake 的支付回调现在会校验 Store ID，后台「Waffo Pancake」设置里的 Store ID 必须已填写且和商户后台一致，否则订单回调会被拒绝、充值不到账。

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
2. **从带「限期奖励」的版本（第十六节）回滚再升级回来之前，先关掉还没过期的限期奖励**：旧版本扣费和转移邀请额度时不会记账到限期奖励上，回滚期间用掉的奖励在新版本看来还是「没用过」，再升级后到期时会被当作未使用的部分扣掉（扣到的就是充值的钱）。回滚后、再次升级前执行一次（只是放弃这些奖励的过期，不扣任何额度；`<当前 unix 时间>` 用 `date +%s` 得到）：

   ```sql
   UPDATE bonus_grants SET status = 'expired', deducted_quota = 0, closed_at = <当前 unix 时间> WHERE status = 'active';
   ```

   这些奖励之后就和普通余额一样永不过期。没有回滚过的站点不需要做。
3. **数据库 schema 不会自动回退**。当前定制版基于 `v1.0.0-rc.42`。rc.41 启动时新建 `user_access_tokens` 表，并在 `options` 表写入一行 `LegacyAccessTokenRetireAt`（见第五节）；rc.40 启动时会做一次 `options` 表主键修复、新建审计日志/账号安全/请求策略等多张表并给若干列改类型；回滚到 rc.40 之前的镜像时这些新表和列会留在库里——通常是加法式变更，旧版本能正常跑；万一起不来，用切换前的 `mysqldump` 恢复。

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
| 回复被截断，客户端却当成已经答完 | 上游（中转站空闲超时、Cloudflare、Kiro 封号等）在发出结束事件前断开时，网关仍按正常结束收尾：OpenAI 补 usage 和 `[DONE]`，Claude 既没有 `message_stop` 也没有错误，Responses 没有 `response.completed`；Responses 上游发出 `error` 或 `response.cancelled` 事件后，转成 Chat / Claude 格式的流同样补一个正常结束；Gemini 流里的 `{"error":…}` 被直接丢弃 | 在结束符前补发一条客户端协议的流内错误（code `upstream_stream_interrupted`，502），日志记一行 `stream interrupted: upstream ended before completion (reason=…)`，消费日志的 `stream_status.response_status` 为 `failed`；已输出的部分照常计费，也不会重试。Responses 上游的 `error` / `response.cancelled` 事件也作为流内错误发给 Chat / Claude 客户端（`error` 带上游的错误信息）。Gemini 流内错误按上游给的状态码和信息报给客户端 |
| Gemini 渠道思考和正文混在一起、正文跑进思考框 | Gemini 同一个 chunk 里同时有 thought 和答案时，整个 chunk 被标成 `reasoning_content` | thought 进 `reasoning_content`，答案进 `content` |
| 上游以裸 `[DONE]` 结束时报错 | 解析器把裸 `[DONE]` 切成 `]` 交给适配器 | 正确识别为结束 |
| 生成很久后最后一个 chunk 发不出 | 30 秒写超时只在循环内续期，收尾写入可能撞上过期的超时 | 每次写入都续期 |
| 开了「思维链转内容」时思考文字混进正文 | 第一个思考 chunk 若同时带正文会丢正文；正文之后再出现思考不会重新加 `<think>` | 都处理了，交错思考也能正确包 `<think>` |

**上线后怎么确认修好了：**

```bash
# 以前出问题的请求会有这一行；修复后，已开始输出的流不会再出现它
docker compose logs new-api 2>&1 | grep '重试：'
# 流中途出错时的记录（配合 request id 看 received= 是否大于 0）；上游流被截断时是 stream interrupted:
docker compose logs new-api 2>&1 | grep -E 'relay error:|stream ended: reason=|stream interrupted:'
```

如果 `重试：` 仍然频繁出现（针对还没输出内容的请求，这是正常重试），说明某个渠道经常在建连阶段就失败，去后台看渠道的错误日志。

**仍然属于上游/模型行为、网关不负责的：** Claude 4 的交错思考（thinking → text → thinking 是模型真实输出）；模型自身的复读/死循环；上游返回 `finish_reason: length`（`max_tokens` 不够）。这类情况修复后仍会原样透传。

---

## 八、分组充值门槛（只有累计充值够的用户才能用某个分组）

**在哪设置：** 系统设置 → 计费 →「分组准入」。每条规则包括三项：

| 字段 | 含义 |
|---|---|
| 分组 | 要限制的分组（下拉选择，不能选 `auto`） |
| 累计充值门槛 | 和充值页输入的「充值数量」同一个单位（例如「最低充值 50」的那个 50）。填 `0` 表示**只有白名单里的用户能用** |
| 白名单用户 | 每行一个，或用逗号、空格分隔。纯数字按**用户 ID** 处理，其它按**用户名**处理；用户名本身是数字时在前面加 `@`（`@123` 表示用户名 123）。保存时服务器把用户名换成 ID，之后**只按 ID 判断**（用户改名不影响，别人改成同名也冒充不了） |

另有一个开关「兑换码计入充值」（默认开）：用户兑换过的兑换码按额度折算后计入累计充值。卡密是靠卖的就保持开启；如果兑换码多是免费发放的，可以关掉，再把需要的人加白名单。

**谁能用被限制的分组（满足任一即可）：**
1. 管理员和超级管理员；
2. 用户自己的分组就是这个分组（你在用户管理里把他设成这个分组，或订阅套餐把他升级到了这个分组）；
3. 在这条规则的白名单里；
4. 累计充值 ≥ 门槛。

**累计充值怎么算：** `top_ups` 表里所有**成功**订单的充值数量之和（Creem 订单按额度折算；订阅套餐的购买记录不算），加上（开关开启时）用过的兑换码额度。`top_ups` 表不受「清理历史日志」影响，清日志不会让用户的累计充值清零。**后台手动给用户加的额度不算**：线下收款、手动加额度的用户请加白名单。

**用户那边看到什么：**
- 创建/编辑令牌时，不满足条件的分组显示为灰色锁定，并写明原因（例如「需累计充值 50（当前 30）」）；
- 模型广场里被限制的分组带「累计充值 ≥ 50」或「受限」（仅白名单）标记，价格照常展示；
- 已有令牌绑定在被限制的分组上、但用户不满足条件时，调用返回 HTTP 403：`分组 svip 需要累计充值满 50 才能使用（当前累计 30）`。**开启规则前想清楚这一点。**
- 令牌选 `auto`（自动分组）时，不满足条件的分组会被自动跳过。
- 视频续作、Midjourney 放大/变换等基于旧任务的请求会沿用原任务的渠道；如果这个渠道只服务于用户当前无权使用的分组，请求同样会被拒绝。

**生效时间：** 规则保存后本机立即生效，多节点部署下其它节点最多 60 秒（`SYNC_FREQUENCY`）。用户刚充值后累计金额立即刷新；极少数并发情况下最多延迟 2 分钟（`USER_TOPUP_TOTAL_CACHE_TTL`，默认 120 秒）。

---

## 九、「优先订阅」用完订阅额度不走钱包：定制版做了什么

**现象：** 扣费偏好为「优先订阅」的用户，订阅额度不够时报 `订阅额度不足或未配置订阅: subscription quota insufficient, need=5000`，而不是改扣钱包。

**根因（上游 bug）：** 套餐的「额度用尽后允许使用钱包余额」会在购买时复制到用户订阅上（`user_subscriptions.allow_wallet_overflow`）。这一列是 rc.12 才加的，老订阅在加列时是 NULL（等于允许）。但上游每次扣订阅额度都是整行保存，Go 把 NULL 读成 `false` 再写回去，**老订阅第一次被使用就被改成了「不允许回退钱包」**；只要用户还有一个这样的有效订阅（试用套餐有效期到 3025 年），就永远不会再回退钱包。另外，后台修改套餐的这个开关，也不会影响已经买了的人。

**定制版的修复：**
1. 扣减、重置订阅额度时只更新变动的列，不再把 NULL 改成 0；
2. 判断能否回退钱包时看**套餐当前的设置**（开着就允许），而不是购买时的快照；只有套餐已被删除时才看快照。所以你在后台改套餐开关，立即对所有已购用户生效；
3. 如果套餐确实关掉了「额度用尽后允许使用钱包余额」，用户会看到更明确的提示：`订阅额度不足，且当前订阅套餐不允许额度用尽后使用钱包余额`。

升级后无需改数据，之前被误改成 0 的订阅会自动恢复回退钱包（只要它的套餐开关是开着的，默认就是开着）。

**另一个相关问题也一并修了：** 请求实际花费超过订阅剩余额度时（预扣时够、结算时不够），上游会结算失败，超出部分既不扣订阅也不扣钱包。现在订阅先扣到用完，超出部分在套餐允许时转扣钱包（和钱包结算一样，余额不足会记为欠费），这部分记录在该条使用日志数据的 `wallet_quota_deducted` 字段里；套餐不允许回退钱包时，超出部分仍不收费，但会在服务器日志里记一条。

**想在升级前先确认某个用户的情况（MySQL）：**

```sql
SELECT us.id, sp.title, us.amount_total - us.amount_used AS remain,
       us.allow_wallet_overflow AS sub_flag, sp.allow_wallet_overflow AS plan_flag
FROM user_subscriptions us LEFT JOIN subscription_plans sp ON sp.id = us.plan_id
WHERE us.user_id = <用户ID> AND us.status = 'active' AND us.end_time > UNIX_TIMESTAMP();
```

`sub_flag = 0` 而 `plan_flag` 是 1 或 NULL，就是被上游 bug 误改的订阅，升级后自动恢复。`plan_flag = 0` 说明套餐本身关掉了回退钱包，到后台「订阅套餐」里打开「额度用尽后允许使用钱包余额」即可。

---

## 十、渠道限额（按额度或次数，用满就停用，可每天重置）

**在哪设置：** 渠道管理 → 渠道那一行的操作菜单 →「额度限制」。可以设置：

| 项 | 说明 |
|---|---|
| 用量限额 | 这个渠道最多能消耗多少额度（和「已使用」列同一个单位，例如 $1000）。填 0 = 不限 |
| 次数限额 | 这个渠道最多能处理多少次计费请求（例如 500 次）。填 0 = 不限。两个限额可以只设一个，也可以都设，**任意一个先用满就停用** |
| 每日重置 | 打开后按天计算：每天 0 点（容器时区，compose 里是 `TZ=Asia/Shanghai`）额度和次数都清零，第二天又能用到限额 |
| 自定义报错 | 分组里的渠道都达到限额时返回给调用方的提示，留空时默认是「该分组已达限额，请切换为其它分组」 |

弹窗里能看到当前用量，也可以点「重置用量」手动清零。渠道列表的状态列会显示「已达上限」，「已使用/剩余」列下面会显示「限额 已用 / 上限」（开了每日重置还会带「每天」标记）。

**达到限额后会怎样：**
- 这个渠道不再被选中，同分组的其它渠道照常使用（优先级低的渠道会顶上）；
- 分组里所有渠道都达到限额时，调用返回 HTTP 429，`code` 为 `channel_quota_exhausted`，`message` 为你设置的自定义报错；
- 令牌用 `auto` 分组时会自动换到下一个分组，全部分组都用不了才报错；
- 基于旧任务的续作/放大请求，如果原渠道已达限额，也会返回这个报错。

**注意：**
- 额度按每次请求**结算后的实际扣费**累计（退款会减回去）；次数按**完成的请求**累计，每个请求算 1 次（价格为 0 的请求也算），异步任务失败退款时次数减回去，结算时补扣差额不算新请求。并发很高时可能略超限额（最后几个同时进行的请求会全部完成）；
- 不扣费分组（第十一节）的请求同样计入渠道的额度和次数；
- 只统计**设置限额之后**的用量；「已使用」列里的历史总用量不受影响；
- 多节点部署时，其它节点最多 10 秒后看到最新用量。

---

## 十一、不扣费分组（福利分组）

**在哪设置：** 系统设置 → 计费 →「分组计费」。每个分组一个开关：**扣费**（默认）/ **不扣费**。

**设为不扣费的分组：**
- 用户调用时**不扣**钱包余额、不扣订阅额度、不扣令牌额度；余额是 0 的用户也能用；
- **照常累计**：渠道的「已使用」、渠道限额（第十节）的用量、使用日志里的花费（日志里标「免费分组」），用户的「已用」统计也会增加；
- 模型广场里这个分组带「不扣费」标记。

**建议搭配：** 给这个分组的渠道设一个**每日重置的渠道限额**（第十节），就是「每天限量的免费福利」，用完了当天自动停，第二天恢复；再配合分组准入（第八节）控制谁能用。

**注意：**
- 按「最终实际使用的分组」判断：令牌用 `auto` 时，如果实际落在不扣费分组，就不扣费；先在收费分组预扣、重试后落到不扣费分组的，预扣会全部退回；
- **Midjourney 请求不受这个开关影响，照常扣费**（它的退款逻辑会直接退钱包，免费会被反向利用）；
- 令牌本身设置了额度上限且已用完的，仍会被令牌额度检查拦下。

---

## 十二、旧定价一键转为计费表达式

**升级后先检查一件事：** 以前在模型定价编辑器里点到「计费表达式」标签页时，会自动填一个占位表达式 `tier("base", p * 0 + c * 0)`；如果当时直接保存，这个模型就会**按 $0 计费**（表达式优先于旧的按次/按量价格）。新版已修复这个问题，「全部转换」时会把这类模型单独列为「可疑」，并按它们还保存着的旧价格重新转换。

**怎么转换：** 系统设置 → 计费 →「模型定价」→「全部转换为计费表达式」。
1. 先弹出预览，列出：将要转换的模型（及生成的表达式）、可疑的 $0 表达式（及替换成什么）、跳过的模型（及原因）；
2. 确认后一次性保存（和手动保存走同一个事务，有人同时在改定价会提示冲突、什么都不写）；
3. 旧价格仍然保留，单个模型想切回旧模式还能切回去。

**转换规则：** 按次模型（设置了固定价格）转成 `tier("request", fixed(价格))`；按量模型按倍率转成 token 单价表达式（含缓存等）。规则和单个模型的「转换为计费表达式」完全一样。

**不能自动转换、会被跳过的：** 异步任务模型、视频模型、名字带 realtime 的模型、OpenRouter 上默认价格的 Claude、Gemini/OpenAI 音频价格冲突的模型、只有补全倍率没有输入价格的模型、模型映射有问题的模型。这些请在编辑器里手动处理。

**模型广场的分类：** 表达式只按次收费（全部是 `fixed(...)`）的模型归入「按次计费」并显示每次价格；按 token 的表达式归入「按量计费」；按次和按 token 混合的仍算按量计费。


---

## 十三、长请求保活（每隔 N 秒发送心跳，和 OpenRouter 一样）

**解决什么：** 推理模型、画图、视频等请求可能几十秒甚至几分钟才出第一个字。这期间连接上一个字节都没有，客户端（或宝塔/nginx/CDN 等反向代理）会按「读超时」把请求断掉，用户看到的是超时报错，而上游其实还在生成。

**在哪设置：** 系统设置 → 模型 → 全局模型配置：
- 「保持连接心跳」：总开关，打开后**流式请求**生效；
- 「Ping 间隔（秒）」：连续这么多秒没有任何输出才发一次，范围 1–3600，**建议填 10**。**不要用默认的 60**：nginx（宝塔）`proxy_read_timeout`、AWS 负载均衡的空闲超时默认都是 60 秒，60 秒才发第一个心跳就晚了（设为 ≥60 时页面会给出提示）；
- 「非流式请求也保活」：总开关打开后才能勾选，打开后**非流式请求**也生效。

**会发什么：**

| 请求类型 | 发送内容 | 客户端看到的 |
|---|---|---|
| 流式 · Claude 格式（`/v1/messages`） | `event: ping` + `data: {"type": "ping"}` 事件，和 Anthropic 官方 API 自己发的一字不差 | Anthropic 官方 SDK（Python / Node）在流的任何位置都会跳过它。Claude Code 从 2.1.196 起有「5 分钟收不到事件就断开重发」的空闲看门狗，它只认事件、不认注释行，所以以前的 `: PING` 保不住它；2.1.222 修了「设置了自定义 `ANTHROPIC_BASE_URL` 时，收到 ping 仍按空闲超时断开」的问题，用户要升级到 2.1.222 或更新 |
| 流式 · Responses 格式（`/v1/responses`，Codex） | 第一个事件（`response.created`）之前：`: PING` 注释行；之后：`event: keepalive` + `data: {"type":"keepalive"}` 事件 | Codex 每收到一个 data 事件就重新开始 300 秒空闲计时（注释行不算），不认识的事件类型直接跳过；OpenAI 官方 Node SDK 本身就定义了这个 `keepalive` 事件；Python SDK 把它原样交给调用方（`event.type == "keepalive"`），按 `type` 分支的代码不受影响。第一个事件之前仍发注释行，因为 Python / Node 两个官方 SDK 的 `client.responses.stream()` 在 `response.created` 之前收到任何事件都会直接报错 |
| 流式 · 其他（chat/completions、图片流式） | `: PING` 注释行 | 标准 SSE 解析器（OpenAI 官方 SDK 等）会忽略注释行，实测正常 |
| 非流式（JSON） | 在 JSON 正文前发换行符 `\n` | JSON 允许前导空白，`json.loads` / `JSON.parse` / 各官方 SDK 都能正常解析 |

只在「静默满 N 秒」时才发，正常出字的流不会多出心跳；流式心跳只插在两个事件之间，不会把一个事件拆开。心跳事件和注释行一样不算正文：只发过心跳时仍会换渠道重试。

**Codex 仍可能断开的一种情况：** 上游在 `response.created` 之前就沉默超过 300 秒（上游排队，或第一个渠道一直不回响应头、拖到超时才换渠道）。这一段只能发注释行（原因见上表），Codex 会按空闲超时断开重连。遇到这种情况，让用户在 Codex 的 `~/.codex/config.toml` 里对应的 `[model_providers.<名字>]` 下加 `stream_idle_timeout_ms = 900000`（毫秒）；Claude Code 对应的是环境变量 `CLAUDE_STREAM_IDLE_TIMEOUT_MS`。

**适用范围：**
- 流式：OpenAI 格式（chat/completions、responses、图片流式）、Claude 格式；
- 非流式：OpenAI 格式（chat/completions、responses、图片生成/编辑、embeddings、rerank）、Claude 格式，以及**插件接管的同步任务**（如视频/图片插件在 `/v1/responses`、`/v1/images/*` 上的同步请求，这类请求可能要等几分钟）；
- **不发**心跳：音频（TTS 返回二进制、转写可能是纯文本；只有 `stream_format: sse` 的流式 TTS 仍沿用旧的 `: PING`）、Realtime、**Gemini 原生格式**（流式：Google 官方 Python SDK 遇到注释行会解析报错；非流式：心跳之后若出错，Google SDK 会静默返回空结果而不报错，比超时更糟）、Midjourney / 视频提交等本来就很快返回的任务接口。

**一定要知道的副作用（和 OpenRouter 相同）：** 心跳一旦发出，HTTP 状态码就已经是 200 了，无法再改。所以：
- 之后如果出错（比如上游 400/429/500），非流式请求会收到**状态码 200 + `{"error": {...}}` 错误体**，流式请求会收到流内的错误事件；
- 实测各官方 SDK 对「200 + 错误体」**都不会抛出原本的错误类型、也不会自动重试**：Python/Node 的 OpenAI SDK 返回 `choices` 为空的对象（再取 `choices[0]` 才报 TypeError），Anthropic SDK 返回 `content` 为空的消息。流式的流内错误事件则能正常抛错。这就是「非流式请求也保活」单独设开关、默认关闭的原因——只在用户确实常被超时困扰时再打开；
- 后台日志、渠道亲和、模型限流统计里记录的仍是真实的错误状态码，不受影响；
- **只发过心跳、还没输出正文时上游失败，仍然会自动重试其他渠道**（以前开了 Ping 之后，只要发过一次心跳就不再重试，这次一并修了）。

**哪些客户端能被救：** 心跳解决的是「读超时」（两次收到数据之间的最长间隔）。实测：Python 的 OpenAI / Anthropic SDK（httpx 读超时）、Node 的 Anthropic SDK、Node 的 OpenAI SDK **流式**请求都能被保住；Node 的 OpenAI SDK **非流式**请求用的是「总超时」，心跳救不了，只能让用户调大 `timeout`。Google 官方 SDK（Python / Node）走 Gemini 原生格式，不发心跳，也只能调大超时。

**反向代理：** 心跳响应都带 `X-Accel-Buffering: no`。只有**一层** nginx（宝塔）直接反代 new-api 时，nginx 会立即转发，不用改配置。

**两层 nginx 串联**（例如 国内宝塔反代 → 美国服务器上的 nginx → new-api）要改外层：nginx 默认不会把 `X-Accel-*` 响应头转给下一层，外层收不到这个头；外层如果还用 nginx 默认的 HTTP/1.0 回源，心跳（连同普通流式输出）会被攒到请求结束才一起发出。在外层 API 所在的 `location` 里加：

```nginx
proxy_http_version 1.1;
proxy_buffering off;
proxy_read_timeout 3600s;
proxy_send_timeout 3600s;
```

或者让外层直接反代 new-api 的 `3009` 端口，去掉中间那层（再用防火墙只放行外层的 IP）。另外宝塔自带的 `/www/server/nginx/conf/proxy.conf` 通常在 http 级别把超时设成 60 秒，超时要写在 API 实际走的那个 `location` 里才稳妥。每一层都用下面的命令核对实际生效的值：

```bash
nginx -T 2>/dev/null | grep -nE "proxy_pass|proxy_(read|send|connect)_timeout|proxy_http_version|proxy_buffering"
```

如果心跳仍被攒住，再检查是否配置了 `proxy_ignore_headers ... X-Accel-Buffering`（会让这个头失效），或者前面还有会缓冲响应的 CDN。

**504 / 524 排查：** 心跳只保护「用户 ↔ new-api」这一段，管不到「new-api ↔ 上游」。
- 先看报错是谁发的：响应头里有 `X-Oneapi-Request-Id`，说明是 new-api 转发的上游错误；没有，就是某层 nginx / CDN 自己产生的。
- `Deadline expired before operation could complete.`（code 504）：Google（Gemini / Vertex）服务端的截止时间到了。在该渠道 → 请求与响应 → 覆盖规则 → 请求头覆盖 里加 `{"X-Server-Timeout": "1700"}`（单位秒，要小于 `RELAY_RESPONSE_HEADER_TIMEOUT`，默认 1800），先在一个渠道上观察效果。第三方中转渠道要对方把这个头转发给 Google 才有效。
- 524 只有 Cloudflare 会发：通常是挂在 Cloudflare 后面的上游中转站约 100 秒没输出。让用户对长回答用流式，或把这类模型换到不走 Cloudflare 的渠道。
- 504 / 524 **不会**换渠道重试（请求可能其实已在上游完成，重试会重复计费、等待时间翻倍）。确实要重试，可在渠道的「状态码映射」里写 `{"524":"502"}`，并把「重试次数」设为 ≥1。
- 在 compose 里加 `ERROR_LOG_ENABLED=true` 后，使用日志 → 通用日志 → 类型「错误」能看到每次失败的渠道、模型、是否流式和用时；也可以先用 `docker logs new-api-alpha-us 2>&1 | grep -E "status code: (504|524)"` 按渠道统计。
- 用户等了约 60 秒就报错：多半是某层 nginx 还是默认 60 秒；约 100–125 秒：Cloudflare；约 300 / 600 秒：Google 默认截止时间或客户端自己的超时。
- `STREAMING_TIMEOUT` 的单位是秒，写 `1800000` 等于约 20.8 天，想要 30 分钟应写 `1800`。

**客户端断开后，上游请求随之取消：** 以前只有流式请求、而且要等上游开始返回之后，客户端断开才会关掉上游连接；非流式请求（以及上游还没返回响应头的那段时间）里客户端走了，上游照样把整段回答生成完，用户还会被扣费。例如 Claude Code 的非流式回退每 300 秒重发一次，被放弃的那几次都在上游跑完、都计了费。现在客户端一断开（关页面、点停止、客户端超时重发），new-api 立刻取消对应的上游请求，流式和非流式都一样：
- 不换渠道重试（请求策略记录里是 `client_gone`），不记渠道错误日志、不触发自动禁用，也不计入模型健康统计；
- 上游还没开始返回就断开的：预扣额度全额退还，用户不扣费；流式已经在接收上游输出的：和以前一样按已收到的部分结算；
- 日志里是 INFO 级别的 `upstream request cancelled, client disconnected:`、`relay stopped, client disconnected:`，流式是 `stream ended: reason=client_gone`，不会再报成 `scanner error` 或渠道错误；
- `RELAY_RESPONSE_HEADER_TIMEOUT`（默认 1800 秒）和上面的心跳都不变。

计费上的取舍：上游一般仍会按断开前已经生成的 token 向运营方收费，而用户不为放弃的请求付钱，这部分由运营方承担；换来的是上游不再把没人要的回答整段生成完（以前这部分上游全额收费）。前面如果有 nginx，别开 `proxy_ignore_client_abort on`，否则 nginx 不会把客户端断开传给 new-api。

**注意：** 和其他后台设置一样，一旦点过保存，就以数据库里的值为准（代码默认值是：总开关关、间隔 60 秒、非流式关）。

**上线后怎么确认生效（把 `<令牌>`、`<模型>` 换成实际值，挑一个首字较慢的模型）：**

```bash
# 流式：首字出来之前应每隔 N 秒看到一行 ": PING"
curl -N https://<你的域名>/v1/chat/completions \
  -H "Authorization: Bearer <令牌>" -H "Content-Type: application/json" \
  -d '{"model":"<模型>","stream":true,"messages":[{"role":"user","content":"写一首长诗"}]}'

# Claude 格式：首字出来之前应每隔 N 秒看到 "event: ping" 和 "data: {"type": "ping"}" 两行
curl -N https://<你的域名>/v1/messages \
  -H "x-api-key: <令牌>" -H "anthropic-version: 2023-06-01" -H "Content-Type: application/json" \
  -d '{"model":"<模型>","max_tokens":4096,"stream":true,"messages":[{"role":"user","content":"写一首长诗"}]}'

# 非流式：等待期间每隔 N 秒收到一个空行，最后是完整 JSON
curl -N https://<你的域名>/v1/chat/completions \
  -H "Authorization: Bearer <令牌>" -H "Content-Type: application/json" \
  -d '{"model":"<模型>","messages":[{"role":"user","content":"写一首长诗"}]}' | cat -A | head
```

---

## 十四、模型广场按「基础模型」合并变体

**效果：** 模型广场里同一个基础模型只显示一张卡片。比如 `[按次]claude-opus-4-6`、`[按次]claude-opus-4-6-thinking-high`、`[正价kiro]claude-opus-4-6-thinking-max` 都归到 `claude-opus-4-6` 这张卡：
- 卡片正面：各线路（名称开头的 `[...]` 标签）及其最低价、变体标签、综合状态条；
- 点「展开」：卡片所在行的下方展开一个整行面板，按线路分组列出每个变体的价格、状态条、延迟、吞吐，「复制」复制的是**完整模型名**，「详情」打开原来的模型详情；
- 表格视图同样按家族一行、点 ▸ 展开；只有一个变体的模型照常显示。

**只影响展示**：用户调用、计费、日志里用的模型名一律不变。

**在哪设置：** 系统设置 → 模型 → 模型广场显示：
- 「合并模型变体」开关（默认开），关掉即恢复成每个模型名一张卡；
- 「变体后缀规则」：从模型名末尾去掉哪些后缀算作变体，以及显示名和说明。默认规则：

| 后缀 | 显示名 | 说明 |
|---|---|---|
| `-thinking` | thinking | |
| `-thinking-minimal / -low / -medium / -high / -xhigh / -max` | thinking-minimal … thinking-max | |
| `-nothinking` | nothinking | 会思考，但不返回思考内容 |
| `-128` | 128 | 思考预算 128（最低思考强度） |

规则从名称末尾按「最长匹配」逐层去除，可以叠加，例如 `-128-nothinking` 显示为「128 · nothinking」。规则顺序就是变体的显示顺序。设置页底部有「预览」框，输入一个模型名能看到它会被拆成什么。

**合并的基础模型（按模型名指定）：** 同页「合并的基础模型」里每行填一个基础模型（不带 `[...]` 线路标签），例如 `gemini-4-argon`。之后名称是「基础模型 + `-` + 任意内容」的模型都会并进这张卡，`-` 后面的部分直接作为变体名：
- `[按次]gemini-4-argon-low`、`[按量]gemini-4-argon-medium` → 卡片 `gemini-4-argon`，线路「按次」「按量」，变体 `low`、`medium`；
- 后面还能叠加上表的规则：`gemini-4-argon-medium-nothinking` 显示为「medium · nothinking」；
- 多个基础模型都匹配时取最长的（同时填 `gemini-4` 和 `gemini-4-argon`，`gemini-4-argon-low` 归 `gemini-4-argon`）；`gemini-4-argonx` 这种只是字母相同的不会被并入；
- 变体顺序：标准在前，接着是只带上表规则后缀的变体（如 nothinking，按规则顺序），然后 minimal / low / medium / high / xhigh / max，其他变体名按字母排。

适合只想合并个别模型、又不想加全局 `-low / -high` 规则（会误伤 `gpt-5.1-codex-max` 这类模型）的情况。

**注意：** 默认规则里**没有**单独的 `-low / -high / -max` 等后缀，因为它们会把本来不同的模型（如 `gpt-5.1-codex-max`、`qwen-max`）误归为变体；需要的话可以在设置里自己加。没有任何规则时，同一个模型不同线路（`[按次]x`、`[按量]x`）仍会合并到一张卡；要完全不合并，请关掉开关。

**分组与价格：**
- 名称不带 `[...]` 的模型，线路名显示它所在的**分组**（如 `Anthropic`），不再显示「默认」；若该分组名正好也是同卡里另一条线路的标签（如 `x` 在分组「按量」、同时还有 `[按量]x`），显示为「默认 · 按量」以免两条同名。
- 同一个模型在多个分组、且各分组算出来的价格不同（分组倍率不同）时，按分组分别列出价格：卡片线路标签、展开面板、表格的价格/缓存列、单模型卡片的「分组定价」都会这样显示。各分组价格相同（含免费、特殊计费表达式）时只显示一份；侧栏选中某个分组时只显示该分组的价格。

**供应商重复：** 旧版本在读取定价时会自动创建供应商，并发时可能建出多条同名记录（侧栏里同一个供应商出现好几次）。现在定价接口会按名称合并，侧栏只显示一次。数据库里的重复记录可以在 控制台 → 模型 → 供应商（`/models/vendors`）里勾选同名供应商后点「合并供应商」彻底清理（可选，不清理也不影响显示）。

---

## 十五、按模型限流（单个模型的请求次数限制）

**在哪设置：** 系统设置 → 安全与限制 → 速率限制，「基于分组的速率限制」下面的「基于模型的速率限制」。点「添加模型」填写：

| 项 | 说明 |
|---|---|
| 匹配方式 | **完全匹配**（默认）：规则名就是模型名，见下一行。**前缀匹配**：名称以规则名开头的模型都算，例如 `[按次]` 匹配 `[按次]claude-opus-4-6`、`[按次]gpt-5` 等这条线路的所有模型。**后缀匹配**：名称以规则名结尾的模型都算，例如 `-thinking`。前缀/后缀看三个名字，对上一个就算：用户请求里写的名字、去掉 `@` 修饰符后的名字、基础模型名（和完全匹配规则用的同一个：去掉 `@` 修饰符，以及 Claude/Gemini 的 `-thinking`、OpenAI 的 `-high` 这类后缀）。所以 `[按次]claude-opus-4-6@effort:high` 也算 `[按次]` 的，`claude-opus-4-6-thinking` 和 `claude-opus-4-6-thinking@effort:high` 都算 `-thinking` 的。后缀规则只看名字：`claude-opus-4-6@effort:high` 虽然也开了思考，但名字里没有 `-thinking`，不算 `-thinking` 的。规则名里不支持 `*` 通配符，用匹配方式来选：前缀末尾或后缀开头带 `*`（如 `[按次]*`、`*-thinking`，也就是表格里显示的样子）保存时会报错 |
| 计数方式 | 只有前缀/后缀规则有。**每个模型各自计数**（默认）：规则相当于给每个匹配到的模型各配了一条同样的规则，`[按次]a` 用完了不影响 `[按次]b`。同一个模型按基础模型名计数（和完全匹配规则一样）：带 `@` 修饰符的写法总是算同一个模型，如 `[按次]claude-opus-4-6@effort:high` 算 `[按次]claude-opus-4-6`；Claude/Gemini 的 `-thinking`、OpenAI 的 `-high` 这类后缀只有名字不带 `[...]` 线路标签时才合并，如 `claude-opus-4-6-thinking` 算 `claude-opus-4-6`，而 `[按次]claude-opus-4-6-thinking` 是单独的一个模型，单独计数。**所有匹配的模型合计**：这个用户调用所有匹配到的模型加起来共用这一条限制 |
| 模型名称 / 前缀 / 后缀 | 前缀/后缀匹配时这一栏叫「前缀」「后缀」，填规则名，如 `[按次]`、`-thinking`。完全匹配时填用户请求里写的模型名（模型映射之前的名字）。开头的 `[...]` 线路标签也是名字的一部分：`[按次]claude-opus-4-6` 和 `claude-opus-4-6` 是两条不同的规则。带 `@` 修饰符或 `-thinking`、`-high` 这类后缀的写法（如 `gpt-4o@temperature:1`、`claude-opus-4-6-thinking`）走的是同一个模型的渠道，所以也算进这个模型的规则；除非给这个写法单独配了规则，那就只算它自己的 |
| 时间窗口（分钟） | 1 到 1440（一天），每条规则可以和全局的「限制周期」不同 |
| 最大请求数（包括失败） | 窗口内**每个用户**最多请求多少次，失败的请求也算。填 0 = 不限 |
| 最大成功数 | 窗口内**每个用户**最多成功多少次，失败的请求不算。填 0 = 不限 |

也可以点「JSON 模式」直接编辑，格式：

```json
{
  "[按次]claude-opus-4-6": {"duration_minutes": 1, "total_count": 5, "success_count": 0},
  "gpt-image-1": {"duration_minutes": 1440, "total_count": 0, "success_count": 20},
  "[按次]": {"duration_minutes": 1, "total_count": 8, "success_count": 8, "match": "prefix", "shared": false},
  "-thinking": {"duration_minutes": 60, "total_count": 100, "success_count": 0, "match": "suffix", "shared": true}
}
```

- `match`：`"exact"`（不写也是完全匹配）、`"prefix"`（前缀）或 `"suffix"`（后缀），区分大小写；
- `shared`：只对前缀/后缀规则有效，`false`（不写也是）= 每个模型各自计数，`true` = 所有匹配的模型合计；完全匹配的规则不能写 `true`（保存时会报错）。

改完点「保存速率限制」（只保存改过的项）。最多 1000 条规则，两个次数都是 0 到 100,000,000。同一个名字只能有一条规则，不管是哪种匹配方式。

**升级说明：** 以前配的规则都没有 `match`，升级后照旧是完全匹配，计数也接着算，不用改。比如以前配了 `"[按次]": {...}` 想限制整条线路却没生效，就是因为它是完全匹配（只匹配名字正好叫 `[按次]` 的模型）：编辑这条规则，把匹配方式改成「前缀匹配」即可，次数设置保持不变（编辑框里输入一个只有 `[...]` 的名字时也会提示）。

**怎么生效：**
- 只在打开「启用速率限制」开关时生效；
- **每个用户单独计数**：A 用户用完了不影响 B 用户，同一用户调用其它模型也不受影响（「所有匹配的模型合计」的前缀/后缀规则除外，它管的那些模型合计）；
- 一个请求可能同时匹配几条规则：完全匹配的那条（按上面的顺序找到的第一条）加上所有匹配的前缀/后缀规则。**这些规则都生效**，各按各的时间窗口和次数计数，先检查完全匹配规则，再按规则名排序检查前缀/后缀规则，哪条先用完就被哪条拦下，所以**最严格的先拦**。例如 `[按次]` 前缀规则「1 分钟 8 次」、`[按次]claude-opus-4-6` 完全匹配规则「1 分钟 3 次」：这个模型每分钟最多 3 次，`[按次]` 线路的其它模型每个最多 8 次（各自计数时）；被后面的规则拦下的请求和失败请求一样，计入前面已经检查过的规则的总请求数，但不算成功数；
- 和全局/分组限制**同时生效**，两边都没超才放行，所以**更严格的那个先拦**。例如分组限制「1 分钟 100 次」、`gpt-4o` 规则「1 分钟 10 次」：这个用户 1 分钟内最多调 10 次 `gpt-4o`，所有模型加起来最多 100 次；被模型规则拦下的请求和失败请求一样，计入全局/分组的总请求数；
- 超限返回 HTTP 429，提示写明模型和规则，例如「模型 gpt-4o 已达到请求数限制：1 分钟内最多请求 10 次，包括失败次数」；前缀/后缀规则写成 `[按次]*`、`*-thinking` 的样子，例如「模型 [按次]gpt-5 已达到规则 [按次]* 的请求数限制：1 分钟内最多请求 8 次，包括失败次数」，合计计数的规则会注明「该规则匹配的所有模型合计」；
- Responses WebSocket（`/v1/responses` 长连接）里每个 `response.create` 按它请求的模型单独计数；因为连接已锁定到别的模型、或令牌不允许这个模型而被拒绝的请求不算（和 HTTP 一样，HTTP 上这类请求在选渠道时就被拒了）。

**例子：**
- `[按次]claude-opus-4-6` 每人每分钟最多 5 次：时间窗口 1，最大请求数 5，最大成功数 0；
- `gpt-image-1` 每人每天最多成功出图 20 次，失败不算：时间窗口 1440，最大请求数 0，最大成功数 20；
- 某模型每人每小时最多请求 200 次、其中最多成功 100 次：时间窗口 60，最大请求数 200，最大成功数 100；
- `[按次]` 线路的每个模型每人每分钟最多 8 次、最多成功 8 次：匹配方式「前缀匹配」，规则名 `[按次]`，计数方式「每个模型各自计数」，时间窗口 1，最大请求数 8，最大成功数 8；
- `[按次]` 线路所有模型加起来每人每分钟最多 8 次：同上，计数方式改成「所有匹配的模型合计」。

**注意：**
- 开了 Redis 时多个节点共用计数，计数的键在规则的时间窗口内没有请求就会自动过期，删掉的规则不会一直占着 Redis；没开 Redis 时每个节点各自计数（和全局限流一样）；
- 没开 Redis 时修改某条规则的时间窗口，这条规则的计数会从 0 重新开始；
- 改一条规则的匹配方式或计数方式，它的计数也会从 0 重新开始（不同方式的计数是分开存的）。

---

## 十六、签到与邀请奖励：付费翻倍、邀请充值门槛、奖励过期

**在哪设置：**
- 签到：系统设置 → 计费 →「签到奖励」（打开「启用签到功能」后才显示下面这些项）；
- 邀请：系统设置 → 计费 →「额度设置」，在「邀请者奖励」「受邀者奖励」下面。

下表里的「额度」都是内部额度单位（和「新用户额度」同一个单位，默认 500000 = $1），输入框下面会显示换算后的金额。

| 设置项 | 选项键 | 默认值 | 取值 | 含义 |
|---|---|---|---|---|
| 付费用户充值门槛 | `checkin_setting.paid_threshold_quota` | 1000000（$2） | 0 ～ 9007199254740991 | 累计充值达到这个额度的用户算付费用户；`0` = 只要充值过就算 |
| 付费用户奖励倍率 | `checkin_setting.paid_multiplier` | 2 | 1 ～ 100，最多两位小数（如 1.5、1.25） | 付费用户的签到奖励 = 随机额度 × 倍率，向下取整；`1` = 和免费用户一样 |
| 免费用户奖励有效期（天） | `checkin_setting.free_expire_days` | 30 | 0 ～ 3650 | `0` = 永不过期 |
| 付费用户奖励有效期（天） | `checkin_setting.paid_expire_days` | 30 | 0 ～ 3650 | `0` = 永不过期 |
| 只有不使用才过期（签到） | `checkin_setting.expire_only_when_unused` | 关 | 开 / 关 | 见下文 |
| 邀请奖励充值门槛 | `InviteRechargeThresholdQuota` | 0 | 0 ～ 9007199254740991 | 受邀者累计充值达到这个额度后，才同时发放邀请者和受邀者的奖励；`0` = 注册时立即发放（和以前一样） |
| 邀请奖励有效期（天） | `InviteRewardExpireDays` | 30 | 0 ～ 3650 | 邀请者奖励和受邀者奖励的有效期；`0` = 永不过期 |
| 邀请奖励只有不使用才过期 | `InviteRewardExpireOnlyWhenUnused` | 关 | 开 / 关 | 见下文 |
| 邀请者奖励 | `QuotaForInviter` | 250000（$0.5） | ≥ 0 | 见「升级说明」；邀请额度不足 1 个单位时可以一次全部转移，见下文 |

超出取值范围的保存会被拒绝并提示原因。

**付费用户怎么算：** 累计充值 = 所有**成功**的在线充值订单（易支付、Stripe、Creem、Waffo、Waffo Pancake，以及管理员在充值记录里手动「补单」的订单）按到账额度计算，再加上用过的兑换码额度（兑换码被删除了也算）。**不算**：订阅套餐的购买（包括用余额买套餐）、后台手动加减额度、签到和邀请奖励、邀请额度转移、请求退款。每次签到时按当时的累计充值判断，充值后下一次签到就生效。

和「分组准入」（第八节）的累计充值有两点不同：这里按**到账额度**算（Stripe 订单按支付金额 × 每单位额度，Creem 订单按订单额度，其它按充值数量 × 每单位额度，和充值到账时的算法一样），历史订单按**当前**的「每单位额度」折算；兑换码总是计入，不受分组准入里「兑换码计入充值」开关影响。

**签到奖励：** 免费用户拿到「最小～最大签到额度」之间的随机额度；付费用户拿到随机额度 × 倍率（向下取整，不会少于随机额度）。签到日历里记录的是实际到账的额度。

**邀请充值门槛：**
- 门槛为 0：和以前一样，注册时立即发放两份奖励；
- 门槛大于 0：注册时只记下这次邀请（奖励金额按**注册时**的「邀请者奖励」「受邀者奖励」设置记下），受邀者**累计充值第一次达到门槛**时，两份奖励一起发放，只发一次。检查时机：受邀者每次充值成功后、受邀者签到时；管理员调低门槛或重新确认合规条款后，主节点在约 1 分钟内补发所有已达标的邀请（在从节点上保存的设置，主节点要等设置同步，最多再晚 `SYNC_FREQUENCY` 秒）；
- 「推荐计划」里的「邀请」人数在注册时就加 1（和以前一样），奖励要等达标后才到账；
- 只有在「支付网关」里确认了合规条款时才会**记录和发放**邀请奖励：未确认期间注册的邀请不会被记录（「邀请」人数也不加），之后确认也**不会补发**；已经记录但还在等待的邀请（门槛还没达到，或记录后又取消了合规确认），重新确认后由主节点在约 1 分钟内补发，或在受邀者下次充值、签到时发放；
- 有效期从**发放**时开始算，用的是发放时的「邀请奖励有效期」。

**奖励过期规则：**
- 有效期大于 0 的签到奖励、受邀者奖励、邀请者奖励会被记录为「限期奖励」，有效期为 0 的奖励就是普通余额，永不过期；
- 钱包里的限期奖励（签到奖励、受邀者奖励）**优先被消费**，按「发放时间 + N 天」先到期的先用（不管「只有不使用才过期」开没开，消费顺序都一样，所以切换开关不会把已经发生的消费挪到别的奖励上）；只有走**钱包**扣费的消费才算（API 请求扣钱包、订阅额度用完后转扣钱包、用余额购买订阅套餐都算）；订阅额度内的消费、免费分组的请求不算；
- 过期时只扣掉**还没用完**的部分，扣到 0 为止，**不会扣到充值的钱**：例如 A 充值 $2、签到得到 $0.02 的奖励，期间从钱包花了 $0.012（先用的是奖励），到期时只扣剩下的 $0.008，余额正好回到充值的 $2；如果期间花了 $0.05，奖励已经用完，到期时什么也不扣；
- 邀请者奖励在「推荐计划」的邀请额度里，转移到余额时**优先转移限期奖励**（按「发放时间 + N 天」先到期的先转），转到余额后就是普通余额，永不过期；到期时从邀请额度里扣掉还没转移的部分；
- **转移数量：** 每次至少转移 1 个单位（`QuotaPerUnit`，默认 $1，和以前一样），**或者一次转出全部邀请额度**：邀请额度不足 1 个单位时只能全部转出，不能只转一部分（接口返回「邀请额度不足 … 时只能全部转移」）。所以新装默认的 $0.5 邀请者奖励即使只有一份，也能在过期前转到余额；
- 过期在截止时间后约 1～2 分钟发生（开了批量更新时再加 3 × `BATCH_UPDATE_INTERVAL` 秒，本仓库的 `deploy/docker-compose.yml` 是 30 秒，也就是约 2.5～3.5 分钟）；
- 每发放、过期一份奖励，用户的使用日志（系统类型）里都有一条记录，例如「用户签到（付费用户 ×2），获得额度 $0.02，将于 2026-11-23 10:05 过期」「签到奖励 $0.02 已过期，扣除未使用额度 $0.008」。

**「只有不使用才过期」开关（签到、邀请各一个）：**
- 关（默认）：从发放时起满 N 天就过期，不管用没用；
- 开：N 天内**一次都没有使用**才过期，**每使用一次都重新计算 N 天**。页面上显示「N 天内未使用将过期」和「当前到期时间」；
- 什么算使用：签到奖励和受邀者奖励——这个用户任何一次钱包扣费（同上，订阅额度内的消费和退款不算；预扣后又全部退回的请求也算用过一次）；邀请者奖励——把邀请额度转移到余额；
- 一次使用会把这个用户所有同类限期奖励的期限一起往后延；
- 切换开关对**已经发放的奖励**同样生效（下一分钟起）：关 → 开，最近用过的奖励期限按最后一次使用重新计算（只会变长，不会变短）；开 → 关，已经超过「发放时间 + N 天」的奖励在下一分钟过期，哪怕最近刚用过；来回切换不会多扣，因为消费顺序始终按「发放时间 + N 天」（见上文），只有到期时间跟着开关变。

**用户那边看到什么：**
- 个人中心 →「每日签到」：说明里写明付费用户的门槛和倍率、奖励多少天后过期（或 N 天内未使用将过期）；签到成功后显示到账额度、「付费用户 ×2」标记和当前到期时间（永不过期时显示「永不过期」）；
- 钱包 →「限期奖励」：每一份还有剩余的奖励一行，**按使用顺序排列（排在最上面的先用，和「只有不使用才过期」开没开无关）**，显示来源、剩余 / 总额和当前到期时间；右上角「未使用的奖励」是所有限期奖励的合计；最多列出 100 份，超过时列表下面注明「只列出了前 100 份，共 N 份奖励」；没有限期奖励时显示空状态，所有奖励都设成永不过期且没有剩余奖励时不显示这张卡片；
- 钱包 →「推荐计划」：还在等待发放的邀请者奖励份数（只算会给邀请者发奖励的邀请；受邀者账号已删除的不算，这样的奖励永远不会发放，主节点也不再反复检查；有门槛时说明要等受邀者充值达到门槛）、自己作为受邀者还没发放的奖励和门槛、限期的邀请者奖励；转移弹窗里提示「优先转移会过期的奖励」，邀请额度不足 1 个单位时弹窗直接填好全部额度（不能修改）并说明只能全部转出；
- 钱包 →「限期奖励」超过 5 份时只显示最先使用的 5 份，点「显示全部」展开。

**精度说明：**
- **直接写库和批量更新（`BATCH_UPDATE_ENABLED=true`，本仓库的 compose 默认开启）两种模式下一样：** 过期扣除的额度不会超过真正没用完的奖励，充值的钱一分不会被扣。前提是各节点的系统时间误差在 10 秒以内（开 NTP 自动校时）；
- **跨过结算的退款：** 发放签到/邀请奖励、过期检查时会先结算之前的钱包消费，这时还没结束的请求（已经预扣）也会被算成「用了奖励」。系统只记住**最近一次**结算：请求在这次结算之后失败退款时，退款先抵这次结算里用充值的钱付的部分，剩下的还给这次结算用掉的奖励（已用完但还没到期的奖励会重新变成有效）；批量更新模式下退款在写库时按同样的规则处理。请求挂着期间**又发生了一次结算**（例如第二天又签到、这个用户的任何一份奖励（包括邀请额度里的）到了过期检查、发放受邀者奖励），之前那次结算算进去的部分就只能按普通余额退回，那份奖励不再还回去（见下一条）；
- **对用户有利的偏差（用户多留了一些奖励，充值的钱不会多扣）：**
  - Midjourney 的失败退款、升级前提交的异步任务的退款记为普通余额；
  - 一个请求挂着期间发生了两次及以上的结算（见上一条），被它占用的奖励留作普通余额。异步任务可能挂好几分钟，一个预扣额度覆盖所有奖励的失败任务可以一次留下所有奖励；
  - 一个请求在结算**之后 10 秒内**开始、之后退款：各节点的时钟可能差 10 秒，系统分不清它是在结算之前还是之后扣的费，只能按「之前」处理，退款先抵这次结算里用充值的钱付的部分（也就是上一次结算以来用自己余额付的全部消费，通常比奖励大得多），所以最多会留下「退款额」和「这部分」中较小的那么多奖励。签到就是用户自己触发的结算，**不需要掐时间**：签到后马上发一个会失败的请求（画图、异步任务总是预扣），这份签到奖励就不会过期；用自己余额消费过的用户每天这样做，几乎每份签到奖励都能留下来；
  - 过期检查正好结算到一个还没结束的请求、奖励随即过期，这个请求之后退款时，被它占用的那部分奖励不再扣回；
  - 截止时间到过期检查处理完之间（几分钟内）的使用仍会先用掉奖励；
  - 批量更新模式下，签到、发放受邀者奖励时会先把这个用户在**本节点**暂存的消费写库再结算；其它节点上还没写库的消费（最多 `BATCH_UPDATE_INTERVAL` 秒，compose 里是 30 秒）之后才写库，可能被算到新奖励上；
  - 过期扣除不超过当时的余额，余额不足（或为负）时少扣或不扣。

  这些偏差只涉及已经发放的免费奖励，最坏情况就是这部分奖励不过期（和以前没有过期功能时一样）。退款只能按时间判断先后，要完全消除需要按扣费的实际先后顺序匹配退款，现在的实现做不到；如果不能接受「有意这样操作的用户能留下签到奖励」，可以把签到奖励有效期设成 0（永不过期）或调低签到额度；
- 「只有不使用才过期」开着时，使用时间在批量模式下按写库时间算（最多晚 `BATCH_UPDATE_INTERVAL` 秒）；过期检查按主节点的 `BATCH_UPDATE_INTERVAL` 留了余量，不会因为写库延迟把 N 天内用过的奖励提前判过期，所以所有节点的 `BATCH_UPDATE_INTERVAL` 要设成一样。

**升级说明：**
- 升级前的余额、邀请额度**永不过期**，只有升级后发放、有效期大于 0 的奖励才会过期；不需要补数据，累计充值直接从历史充值记录算；
- 数据库变化：`users` 表加 6 列，MySQL 和 PostgreSQL 上用**一条** `ALTER TABLE` 一次加完（MySQL 5.7 和 PostgreSQL 11 以下会因此重建一次 `users` 表，PostgreSQL 11 以下重建期间锁表，用户多时第一次启动会慢一些，建议在低峰期升级、升级前照第二节备份）；新增 `bonus_grants`、`invite_rewards` 两张表；`redemptions.used_user_id` 加索引；
- 升级后又回滚到旧版本、再升级回来之前，照第四节「回滚」的说明先关掉还没过期的限期奖励；
- `QuotaForInviter`（邀请者奖励）：默认改为 250000（$0.5）。以前没在后台保存过这一项的站点，升级后就按 $0.5 发；**保存过的站点保持原来保存的值**（包括保存过 0 的），想改成 $0.5 的话在「额度设置」里自己改；
- 过期检查和补发邀请奖励只在主节点运行（没有设置 `NODE_TYPE=slave` 的节点），开多个主节点也不会重复扣。

**排查：**
- 服务器日志关键字：`奖励过期任务`（英文 `bonus expiry task`）、`处理用户 … 的奖励过期失败`、`发放用户 … 的邀请奖励失败`、`重新检查待发放的邀请奖励失败`；
- 某用户当前的限期奖励（MySQL；PostgreSQL 把 `FROM_UNIXTIME(x)` 换成 `to_timestamp(x)`）：

```sql
SELECT id, source, pool, amount, remaining, status,
       FROM_UNIXTIME(created_at) AS created, FROM_UNIXTIME(expires_at) AS fixed_deadline,
       FROM_UNIXTIME(check_at) AS next_check, deducted_quota
FROM bonus_grants WHERE user_id = <用户ID> ORDER BY id DESC LIMIT 50;
```

`source`：`checkin_free` 签到奖励、`checkin_paid` 付费签到奖励、`invitee` 受邀者奖励、`inviter` 邀请者奖励；`pool`：`wallet` 钱包、`aff` 邀请额度；`status`：`active` 有效、`used` 已用完、`expired` 已过期（`deducted_quota` 是过期时实际扣掉的额度）。表里的 `remaining` 只在发放新奖励和过期检查时结算，钱包页面显示的剩余已经把之后的消费算进去，两者不一样是正常的。

- 还在等充值门槛的邀请：

```sql
SELECT id, inviter_id, invitee_id, inviter_quota, invitee_quota, status,
       FROM_UNIXTIME(created_at) AS registered, FROM_UNIXTIME(granted_at) AS granted
FROM invite_rewards WHERE status = 'pending' ORDER BY id DESC LIMIT 50;
```

受邀者已经达到门槛但还是 `pending`：先确认「支付网关」里的合规条款已确认，再看服务器日志里有没有「发放用户 … 的邀请奖励失败」；重启主节点会在启动后一分钟内重新检查所有待发放的邀请。

---

## 十七、副管理员（不能查看任何充值信息的管理员）

**是什么：** 副管理员也是管理员（数据库里 `users.role` 仍是 10），普通管理员能做的事它都能做，唯独看不到、也不能操作任何充值信息。系统设置仍然只有超级管理员（root）能进。

**副管理员能做的（和管理员一样）：**
- 渠道、模型、分组、令牌、部署等管理，包括查看和刷新上游渠道余额；
- 用户：查看余额（剩余额度）和已用额度，编辑（用户名、显示名、分组、备注）、禁用、启用、删除普通用户，清除普通用户的登录绑定，添加普通用户（新用户赠送额度照常发放）；
- 使用日志（消费、错误、系统等记录）、数据看板、绘图与任务记录；
- root 在权限表里给它勾上「审计日志」后可以看审计日志，充值相关的记录除外；
- 自己的钱包：充值、兑换、购买订阅、查看自己的订单和自己的充值日志，和普通用户一样。

**副管理员看不到、做不了的（菜单和按钮隐藏，直接调接口返回 403）：**
- 「兑换码」页面：查看、生成、修改、删除兑换码；
- 「订阅」页面：查看和编辑套餐，给用户绑定、重置、作废、删除订阅；用户列表里的「管理订阅」；
- 所有用户的充值订单和「补单」（钱包 →「订单历史」里只有自己的订单，也没有补单按钮）；
- 「调整额度」（给用户加额度、减额度、改余额）；
- 使用日志里的「充值」「管理」两类记录：类型筛选里没有这两项，「全部」里也不显示（包括它自己的充值记录，要看自己的切到「仅自己」），导出同样不含；
- 审计日志里充值相关的记录：加减额度、补单、兑换码、订阅管理、充值和支付接口的访问记录；
- 用户列表和用户详情里的 Stripe 客户 ID；
- 重置其他用户的密码、两步验证（2FA）和 Passkey：重置之后就能用这个账号登录，看到这个用户自己的充值订单和充值日志。编辑用户时没有「密码」一栏，操作菜单里没有「重置 Passkey」「重置 2FA」；用户忘了密码或丢了验证器时请管理员或 root 处理。root 在权限表里取消了「充值与订阅管理」查看权限的管理员同样不能重置。副管理员添加用户时设的初始密码它自己知道，请让用户登录后修改；
- 访问令牌里的「充值与订阅管理」「兑换码」权限：创建令牌时不再提供；以前作为管理员创建的令牌即使带着这些权限，调用这些接口也是 403。

**怎么设置（只有 root 能操作，每一步都要二次验证，并写入审计日志）：**
- 新建：「用户」页 →「添加用户」，角色选「副管理员」；
- 普通用户或管理员改成副管理员：用户列表该行的操作菜单 →「设为副管理员」。普通用户从副管理员的默认权限开始，这个 ID 上遗留的单独权限记录一起清掉（和「提升」一样）；管理员保留 root 单独调整过的非充值权限，充值权限去掉；
- 副管理员改回管理员：操作菜单 →「设为管理员」（恢复管理员的全部充值权限）；
- 降为普通用户：操作菜单 →「降级」，副管理员身份一起去掉；
- 「设为副管理员」「设为管理员」「提升」「降级」都会让该用户所有登录会话立即失效，需要重新登录；
- 用户列表的角色一栏显示「副管理员」，角色筛选里可以单独筛「副管理员」；筛「管理员」时不含副管理员；
- 编辑副管理员时，权限表里没有「充值与订阅管理」「兑换码」两行，其它权限照常可以调整；即使通过接口给副管理员勾上充值权限也会被拒绝（「副管理员不能拥有充值权限」），需要看充值信息就改回管理员；
- root 也可以只在某个管理员的权限表里取消「充值与订阅管理」「兑换码」，这个人仍显示为「管理员」；「永远看不到充值信息」的保证只对副管理员成立。

**升级说明：**
- 现有管理员不受影响：主节点启动时会给管理员角色写入「充值与订阅管理」「兑换码」的查看和管理权限，和升级前一样能看能改；不需要迁移数据，数据库只多几行：`authz_roles` 多一行 `deputy`，`casbin_rule` 多几行 `role:admin` 的 `billing`、`redemption` 权限；
- 副管理员身份保存在 `casbin_rule` 表里（`ptype='g'`、`v0='user:<用户ID>'`、`v1='role:deputy'`）。每次判断充值权限都直接读库，设为或取消副管理员后所有节点立即生效；
- 多节点部署：**先升级主节点**（没有设置 `NODE_TYPE=slave` 的节点），**再升级所有从节点，全部节点都换成新版本之后再创建副管理员**。原因：从节点先于主节点升级时，在主节点写入新的管理员权限之前，这个从节点上的管理员会暂时看不到充值信息（最多 `SYNC_FREQUENCY` 秒，偏安全）；还在跑旧版本的节点不认识副管理员，会把副管理员当成普通管理员。单节点部署（本仓库的 `deploy/docker-compose.yml`）升级时不受这一条影响，回滚见下一条；
- **回滚**：换回没有副管理员功能的旧版本之前，先在用户列表里把每个副管理员「设为管理员」或「降级」（用下面「排查」里的 SQL 列出副管理员）。旧版本不认识副管理员，会把他们当成完整的管理员：能看到所有充值订单、兑换码，能加减额度、看充值日志，单节点部署也一样。旧版本上的「降级」也不会去掉副管理员标记。回滚后再升级回来时，用同一条 SQL 查一遍：标记仍在的管理员重新按副管理员处理（只会更受限，想恢复完整管理员就「设为管理员」）；普通用户身上的标记不起作用，「提升」时会一起清掉，也可以直接删掉这一行（`DELETE FROM casbin_rule WHERE ptype = 'g' AND v0 = 'user:<用户ID>' AND v1 = 'role:deputy';`）；
- 访问令牌：「充值与订阅管理」「兑换码」的权限名不变（`billing:read`、`billing:write`、`redemption:read`、`redemption:write`），已有令牌继续可用；「邀请滥用预览」接口（`GET /api/user/invite_abuse/preview`）需要的令牌权限从「用户：查看」改成了「充值与订阅管理：查看」。

**排查：**
- 服务器日志关键字：`读取用户 … 的副管理员标记失败`（英文 `failed to read the deputy admin assignment of user`）。读不到标记时一律按「没有充值权限」处理（管理员也一样），数据库恢复后自动正常；
- 彻底删除用户时，这个用户的权限记录（单独勾选的权限和副管理员标记）和用户在同一个事务里删除；删不掉时整个删除失败、用户还在，可以重试。旧版本彻底删除用户时不删这些记录，而数据库可能把同一个 ID 再分给新账号（例如 MySQL 5.7 重启后），遗留的「允许」记录会让新账号多出权限（只有遗留的副管理员标记是让它更受限）。升级后可以查出所有带用户权限记录的 ID，对照用户列表，把已经不存在的用户的记录删掉：

```sql
SELECT DISTINCT v0 FROM casbin_rule WHERE v0 LIKE 'user:%';
DELETE FROM casbin_rule WHERE v0 = 'user:<已删除用户ID>';
```

- 查看哪些用户是副管理员：

```sql
SELECT v0 FROM casbin_rule WHERE ptype = 'g' AND v1 = 'role:deputy';
```

**另：邀请滥用封禁的人数阈值可以自己设。** 「邀请 ≥ 5 人且被邀请人都没充值 → 邀请人和被邀请人一起封禁」里的 5 是默认值，在 系统设置 →「身份验证」→「邀请滥用」→「邀请人数阈值」里可以改成 3、10 等（1～100000），保存后立即生效。管理员和副管理员不受这条规则影响。

## 十八、OpenAI 渠道内容审查（开启外部审查）与用户安全标识

**是什么：** 类型为 OpenAI（类型 1）的渠道可以打开「开启外部审查」。打开后，每次请求发往这个渠道的上游之前（包括重试到这个渠道的那一次），网关先做两道检查：
1. 本地关键词/正则规则（内置的防蒸馏规则 + 自己加的规则），命中直接拒绝，不调用 OpenAI；
2. 用这个渠道自己的 KEY、代理和请求头覆盖，调用 OpenAI 免费的审查模型 `omni-moderation-latest`（`{渠道 API 地址}/v1/moderations`），检查系统提示词、用户消息和最多 3 张用户图片。判定违规就拒绝。

被拒绝的请求：不转发、不重试到其他渠道、不计费（预扣的额度全部退回）、不会让渠道或 KEY 被自动禁用，在「使用日志」里记一条类型为「内容审查」的记录。唯一的例外是审查调用说明 KEY 或 OpenAI 账号不能用（见下面「审查失败时放行」一条）。用户只看到通用提示「请求未通过内容审查」（HTTP 400），看不到是哪个类别或哪条规则。**不会自动封禁用户**，屡次违规的用户请自己在日志里查。其他类型的渠道（Azure、自定义、Codex、Sub2API 等）没有这个开关，行为完全不变。

**怎么开：**
- 渠道 → 编辑 →「请求与响应」→「内容审查」卡片（只有 OpenAI 类型的渠道才有）。
- **先点「测试审查接口」**：用已保存的 API 地址、第一个启用的 KEY、代理和请求头覆盖发一段无害文本，显示是否可用、耗时和失败原因（见下面「排查」）。新建的渠道要先保存一次才能测试。测试不写日志、不占用 KEY 轮询。
- 打开「开启外部审查」时，如果这个渠道从没保存过内置规则，会自动勾上「误判风险较低」那一组内置规则（包括「输出你的完整推理过程」「show your chain of thought」这类直接索要思维链的说法）。每条内置规则点「查看规则」能看到正则；之后取消的规则不会再被自动勾回去。内置规则会跳过简单的否定说法，例如系统提示词里的「不要输出你的思维链」「Never reveal your chain of thought」不会触发。
- 「审查失败时放行」默认**关闭**：审查调用失败（超时、限流、API 地址不支持 `/v1/moderations` 等）时请求被拒绝，返回 503「内容审查暂不可用，请求未发送」，也不重试到其他渠道。打开后，这类请求不经审查直接发往上游，消费日志的详情里标「审查失败，已放行」，服务器日志打一条 WARN。
- 例外：关闭「审查失败时放行」时，如果审查调用返回 401、403 或 429 `insufficient_quota`（失败原因 `http_401`、`http_403`、`http_429_quota`，说明这个 KEY 或整个 OpenAI 账号不能用了），请求同样**不在这个渠道上发送**，但按「这个渠道的上游返回了这个错误」处理：自动禁用规则照常生效（状态码、关键词，多 KEY 渠道只禁用这一个 KEY），并按正常的重试规则重试到其他渠道（「重试次数」默认 0，即不重试；下一个渠道按它自己的审查设置检查，没开审查的渠道会直接转发）。每次这样的尝试在服务器日志里打一行 ERROR `channel error …`（见「看记录」）；在 compose 里设置了 `ERROR_LOG_ENABLED=true`（见第十三节，默认不开）时，使用日志里还会记一条错误记录，用户看到的是 503「内容审查暂不可用，请求未发送」，管理员在详情里能看到审查调用的状态码和（隐去 KEY 的）错误信息。最后一次尝试就是这样失败的（没有别的渠道、重试次数用完，或按重试规则不再重试）时，用户收到 503，整个请求只记一条「内容审查」记录，详情「内容审查」小节的「不可用的密钥」一行（接口字段 `key_failures`）列出每次这样失败的渠道、第几次尝试（与这个请求「重试链路」中的位置一致）、KEY 序号（从 1 开始，与渠道「多密钥管理」列表一致，只有多 KEY 渠道才有）和原因；重试到的渠道被它自己的审查拒绝时，用户收到那次拒绝（400 或 503），那条「内容审查」记录同样列出之前的「不可用的密钥」；重试到的渠道的上游自己返回错误时，用户收到的是那个渠道的错误，不记「内容审查」记录，之前的 KEY 级失败只留在服务器日志里（设置了 `ERROR_LOG_ENABLED=true` 时还有每次尝试的错误记录）。已经建立的 Responses WebSocket 连接锁定在一个渠道上，不能换渠道，这时只拒绝这一条请求，不禁用 KEY；客户端重连后的第一条请求会走上面的流程。
- 「仅拦截这些类别」：留空表示 OpenAI 判定违规就拒绝；选了类别后只有这些类别违规才拒绝。
- 「不检查图片」：默认每个请求最多检查 3 张用户图片（从最新的开始），打开后图片一律不检查。
- 「每次请求最多审查字数」：默认 10000 字（可设 1000～200000），按每段最多 2000 字切分；一小时内审查过的同样内容不再重复调用、也不占这个额度。OpenAI 按组织限制审查模型，所有 KEY 共用：免费和 Tier 1 大约每分钟 10000 tokens、500 次请求，Tier 2 是 20000 tokens，Tier 3 是 50000 tokens，以组织的 Limits 页面为准。一个汉字大约 1 个 token，英文大约 4 个字母 1 个 token，每张图片多一次请求。设得比 Tier 的每分钟额度还大，只要这一分钟内用满，审查调用就会失败（按上一条的设置拒绝或放行）。这个字数是**成本上限而不是安全保证**：用户可以故意发超长内容让一部分文本不被送审，详见「限制」。
- 「审查超时（秒）」：默认 10 秒，是一次发往本渠道前所有审查调用共用的时限。
- 「自定义规则」：关键词在文本中任意位置匹配，换行当作空格，所以关键词 `chain of thought` 也能匹配被折成两行的 `chain of` / `thought`；正则和内置规则不这样处理，换行仍算换行（正则要跨行请写 `\s`）。正则用 RE2 语法（不支持环视和反向引用），保存时校验。规则匹配的是规范化后的文本：全角字母和标点转为半角，弯引号 `‘’“”` 转为 `'` `"`，各种连字符和破折号（如 `‑` `–` `—`）转为 `-`，连续空白合并成一个，汉字（含中文标点）之间的空格和换行去掉（「别」「別」「勿」前面的空格保留，否定说法靠它识别）。因此「输出你的 完整推理过程」「你的思 维链」和 `don’t`、`chain‑of‑thought` 与不带空格、键盘直接输入的写法效果相同。发给 OpenAI 的文本不做这几步。正则请按规范化后的样子书写，例如写 `思维链!` 而不是 `思维链！`、写 `don't` 而不是 `don’t`、汉字之间不留空格；含全角字符、弯引号、特殊破折号、制表符、连续空格或汉字间空格、因此永远匹配不到的正则会被拒绝保存（`[！!]` 这种带半角备选的写法可以）。最多 100 条，其中正则最多 30 条。规则命中和 OpenAI 判定违规返回给用户的提示完全相同。

**看记录：**
- 使用日志 → 类型选「内容审查」，再按用户名筛选，就能找到屡次违规的用户。
- 详情（违规类别和分数、命中的规则、匹配到的文字（最多 64 字）、审查失败的原因）只有管理员和副管理员能看到；用户自己的日志只显示「内容审查未通过，已拒绝」等一句话和错误码。
- 通过审查的请求在消费日志详情里标「通过」，失败放行的标「审查失败，已放行」；有内容没送审（超出字数、图片超过 3 张、文件/音频等）时标「未完全审查」。
- 服务器日志关键字：`content moderation rejected`（中文日志 `内容审查拒绝了请求`，WARN）、`content moderation failed`（`内容审查失败`，WARN）。被拒绝请求的 `relay error:` 那一行是 INFO，不会触发按 ERROR 报警。KEY 或账号不能用时，每次尝试另有一行 ERROR `channel error (channel #…, status code: 401): status_code=401, content moderation call failed: …`（中文日志 `渠道请求出错 (渠道 #…，状态码 401): …`，后面的 `content moderation call failed` 不翻译），和上游返回错误时一样。日志里不会打印请求内容、规则正则、KEY 或地址。

**用户安全标识（「向 OpenAI 发送用户安全标识」）：**
- 和审查开关相互独立，也只对 OpenAI 类型的渠道有效。打开后，发往上游的 Chat Completions 和 Responses 请求里的 `safety_identifier` 一律换成网关按用户生成的标识（`u_` 加 32 位十六进制，同一用户永远相同，无法反推出用户是谁），客户端自己传的值会被覆盖。OpenAI 据此只封禁违规的那个用户，而不是整个组织。
- 打开「透传请求体」时也生效：只改写 `safety_identifier` 这一个字段，其他字节原样转发。
- 打开后，「允许透传 safety_identifier」不再起作用。
- **请在 `deploy/.env` 里设置一次 `CRYPTO_SECRET`（例如 `openssl rand -hex 32` 生成），之后不要再改**，多节点设成一样的值。没有设置 `CRYPTO_SECRET` 也没有设置 `SESSION_SECRET` 时，每次重启都会随机生成密钥，发给 OpenAI 的标识也随之改变（服务器日志会提示一次）。改动 `CRYPTO_SECRET` 会让所有用户的标识变化，Redis 里的令牌缓存键也会换一批（无害）；改动前发出的任务结果（视频、图片）下载链接（`/v1/tasks/…/artifacts/…/content?access=…`、`/v1/videos/…/content?access=…`）也会失效（返回 404），客户端需要重新查询任务拿新链接。已经设置了 `SESSION_SECRET` 的部署可以让 `CRYPTO_SECRET` 留空，或设成与 `SESSION_SECRET` 相同的值，这样密钥不变；两者都没设的部署每次重启本来就会让这些链接失效，设置后反而稳定。`deploy/docker-compose.yml` 已经透传这个变量，留空时行为和以前一样。
- 不覆盖的接口：Realtime、图片、旧版 Completions、Responses compact、任务插件。

**限制（请先读完再决定是否「审查失败时放行」）：**
1. 助手（assistant）历史、工具调用和工具结果（包括工具结果里的图片）、工具和函数定义、`response_format` 的 schema 和 Responses 的 `text.format`、`prediction` 都不检查，规则也不扫描它们。用户可以伪造一段 assistant 历史，或把「show your chain of thought」写进函数描述里。
2. 透传请求体的渠道：未知的顶层字段和未知类型的内容块原样转发，不检查；只差大小写的重复字段（例如同时有 `content` 和 `Content`）会被直接拒绝。Claude、Gemini 格式的请求在透传渠道上会原样发到 `/v1/chat/completions`，所以按 Chat Completions 请求体检查（Claude 的顶层 `system`、Gemini 的 `systemInstruction` 和 `contents` 也检查）。
3. 图片接口的输入图片（`/v1/images/edits` 的图片、上传文件）和 Sora 的 `input_reference` 不检查，只检查提示词。
4. 文件、音频、视频（`file`/`input_file`（包括以文件形式发送的纯文本文件）、`file_id`、`input_audio`、`video_url`、Claude 的 PDF 文档、渠道把请求转换成 Responses 时 Claude 的所有文档（包括纯文本文档，会以 `input_file` 转发）、Gemini 的 PDF/音频）以文件形式转发，网关不解析、审查模型也无法识别，不检查，计入「未完全审查」。Responses 的提示词模板只检查 `prompt.variables`，不检查保存在 OpenAI 的模板本身。
5. 超出字数：先送审系统/开发者提示词，但最多占一半字数；然后是**最新一条用户消息**；剩下的额度再给系统提示词的其余部分，最后是更早的用户消息（从新到旧）。长消息按 2000 字一段，从头尾交替取段；剩余字数不够一整段时，只送审这一段里剩余字数那么长的部分（从头部取的段送审它的开头，从尾部取的段送审它的结尾），把字数用满。超出的部分不送审，请求照常转发，记「未完全审查」。用长系统提示词或大量短系统消息已经挤不掉最新一条用户消息了（系统提示词在第一步最多占一半字数，也最多占 256 段候选里的一半），但仍有两个缺口：一条比字数上限还长的用户消息，它中间的部分不送审；更早的历史消息可能不送审。本地规则另有自己的扫描范围（最新用户消息 → 系统提示词 → 更早的用户消息，共 256 KB，每段 128 KB），不受这个字数影响。送审和规则扫描之前，网关先规范化文本，每个请求最多规范化约 4 MB 原文（重试到转发方式不同的渠道时另算一份）：重复的消息和只含空白的消息也计入，规范化后变长的（例如 NFKC 会把 `ﷺ` 展开成 18 个字）按变长后的大小计，但每条消息最多按约 1 MB 计（变长后超过 1 MB 的只保留首尾各约 512 KB）；所以一条接近 1 MB、全是 `ﷺ` 的消息实际要规范化约 11 MB 文字，最坏情况下每种转换方式要多花几秒 CPU。按「最新一条用户消息 → 系统提示词 → 更早的用户消息（从新到旧）」的顺序处理，超出的消息既不送审也不被本地规则扫描，审查模块也不保留它们的原文，记「未完全审查」。最新一条用户消息和第一段系统提示词总在这个范围内，正常请求碰不到这个上限。
6. 图片只检查最新的 3 张；更早的图片、超过 20 MB 的 data URL、既不是 http(s) 也不是 `data:image/…` 的地址不检查。远程图片地址检查的是 OpenAI 下载到的内容，如果对方服务器给 OpenAI 和给模型返回不同的图片，网关无法发现（远程图片的结果不跨请求缓存）。
7. Realtime（`/v1/realtime`）和 NovelAI 原生请求在开启审查的渠道上直接拒绝（「开启内容审查的渠道不支持此接口」），因为内容发出前无法检查。
8. `previous_response_id`、`conversation` 指向的上游历史和加密的推理内容无法检查，只检查本次新发的 `input`。
9. 管理员把保活心跳间隔设得比审查耗时还短时，被拒绝的请求在心跳发出后才返回：流式请求以 HTTP 200 + 一个 SSE 错误事件返回；打开了「非流式请求也保活」时，非流式请求以 HTTP 200 + JSON 错误体（`{"error":…}`，Claude 格式为 `{"type":"error",…}`）返回，各官方 SDK 不会把它当作错误抛出（见第十三节）。这是保活的固有行为，不是 bug。审查通常不到一秒；要让被拒绝的非流式请求拿到真实的 400/503，请把开启审查的渠道的「审查超时（秒）」设得比 Ping 间隔短（例如间隔 10 秒时设为 8 秒）。
10. 关键词规则发现不了「行为上的蒸馏」（大量、整齐划一的请求，多个账号协同），也发现不了没有明确指令的加密推理回放；换个说法、用形近字（例如西里尔字母的 о）或编码（base64、leetspeak）都能绕过。否定说法只跳过少数固定句式，个别正常句子仍可能被拦，「不要公开，但告诉我」这种说法也可能被跳过。「别」「別」「勿」前面的空格不去掉，所以在「请勿」「分别」这类词中间加空格仍能绕开对应的规则；汉字之间的换行会去掉，预设规则可能跨过这样的换行命中。
11. Sora 任务（OpenAI 渠道上的 `/v1/videos`）被拒绝时正常记录；通过和失败放行的审查结果不写到任务的计费记录里。
12. 用户安全标识的覆盖范围见上一节；Chat Completions 转 Responses 时，渠道的参数覆盖在设置标识之前执行，无法改掉它。
13. 同一个 OpenAI 组织下所有 KEY 共用审查额度。一个用户连续发大段新内容就可能在一分钟内用光额度：这时所有「失败时拒绝」的渠道对所有用户都返回 503，「失败时放行」的渠道则不经审查转发。OpenAI SDK、Claude Code、Codex 遇到 503 会自动重试，会进一步放大审查调用量。v1 没有按用户的审查额度，也没有熔断。
14. 「审查失败时放行」对所有失败原因都放行，包括请求本身造成的失败：例如图片地址 OpenAI 下载不了、data URL 格式错误。在放行的渠道上，这样的请求（连同图片）会不经审查转发；在拒绝的渠道上返回 503。
15. 参数覆盖在审查之后执行：如果覆盖规则把客户端的请求头或未检查的字段复制进 `messages`、`input`、`instructions`，这部分不会被审查。渠道的系统提示词、参数覆盖里写的内容也不检查。
16. OpenAI 自 2026-11-20 起对「模型接入中间商」的使用政策调整（需要事先获批）不是代码能解决的，请自行评估。

**以后可能加的选项（v1 没有）：** 「无法完整审查时拒绝请求」（超出字数、图片被跳过、有无法检查的内容时一律拒绝）；按用户的审查额度。需要时提出来。

**升级说明：**
- 不需要数据库迁移。设置保存在渠道的 `settings` JSON 里（键名都以 `moderation_` 开头，另有 `send_safety_identifier`），新日志类型的值是 100，存进已有的 `type` 列。
- 不打开开关的渠道行为完全不变。
- 多节点部署：**所有节点都升级之后再打开开关**，还在跑旧版本的节点会不经审查直接转发。开了 Redis 时审查结果在节点间共享。用户安全标识需要所有节点的 `CRYPTO_SECRET` 相同。

**回滚：** 旧版本不认识这些设置，不审查、也不发送用户安全标识；但旧版本在改写渠道设置时（例如上游模型自动更新任务）可能把这些键**丢掉**，再升级回来后请逐个检查开启了审查的渠道。已有的「内容审查」日志会保留，在旧版控制台里显示为类型「未知」，类型筛选选不到。没有其他需要撤销的。

**排查（「测试审查接口」和日志里的失败原因）：**
- `http_401` / `http_403`：KEY 无效或被吊销、账号被封、没有审查权限，或项目的模型白名单里没有 `omni-moderation-latest`。`http_429_quota`：组织额度/余额用完（`insufficient_quota`）。这三种在「失败时拒绝」的渠道上按渠道错误处理（见上面「怎么开」）：打开了自动禁用且状态码在「自动禁用状态码」里（默认只有 401）或错误信息命中禁用关键词时，渠道（多 KEY 渠道是这一个 KEY）会被自动禁用。之后按正常的重试规则处理：「重试次数」≥1 时重试，重试选到的渠道按它自己的审查设置检查；「重试次数」为 0（默认）时不重试，直接返回 503「内容审查暂不可用，请求未发送」。没打开自动禁用时，这个 KEY 不会被禁用，落到它上面的请求每次都先在这里失败一次，然后同样按上面的规则重试或返回 503。默认要找是哪个渠道的哪个 KEY，看两处：请求最终被拒绝时那条「内容审查」记录详情「内容审查」小节的「不可用的密钥」（接口字段 `key_failures`：每次这样失败的渠道、第几次尝试（与「重试链路」中的位置一致）、KEY 序号（从 1 开始）和原因），以及服务器日志里每次尝试的 ERROR `channel error (channel #…, status code: …)` 行（中文日志 `渠道请求出错 (渠道 #…，状态码 …)`；换到别的渠道后成功，或那个渠道的上游自己出错时，只有这一行，它不含 KEY 序号）。在 compose 里设置 `ERROR_LOG_ENABLED=true`（见第十三节）后，使用日志里每次尝试还有一条错误记录（错误码 `content_moderation_unavailable`，详情里有审查失败原因；KEY 序号不在详情里，显示在这条记录「渠道」列的小圆标和悬停提示里，从 0 开始，比「不可用的密钥」里的序号小 1）。「测试审查接口」只测第一个启用的 KEY，多 KEY 渠道里排在后面的坏 KEY 测不出来。
- `http_404`：API 地址没有 `/v1/moderations`（很多转售站、自建网关不支持）。这样的渠道要么打开「审查失败时放行」，要么不要开审查。
- `http_429_rate`：请求或 token 速率超限（见上面的 Tier 额度）；`http_429_size`：单次审查请求就超过了每分钟 token 额度，请调低「每次请求最多审查字数」或提升 Tier。
- `image_rejected`：OpenAI 下载不了用户的图片地址。
- `config`：渠道的请求头覆盖写错了，请修改。
- `timeout`：审查调用超时，可适当调大「审查超时（秒）」。
- `network`：连不上 API 地址或代理配置错误；`http_5xx`：OpenAI 服务端错误；`decode`：返回的不是审查接口的格式（常见于 API 地址指向了别的服务）；`http_other`：其他状态码。
- 被拒绝的请求在服务器日志里有一行 WARN `content moderation rejected a request on channel #…: source=…, detail=…`（来源 `openai`/`rule`/`unavailable`/`unsupported`），对应的 `relay error:` 是 INFO。
