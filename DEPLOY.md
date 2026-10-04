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

当前基线：`v1.0.0-rc.41` + 上游 `main` 到 `1a4166d8e` 的 7 个修复提交（2026-10-03 合并；当时没有比 rc.41 更新的正式版）。rc.41 是 2026-10-01 从 rc.40 合并的，rc.40 是 2026-09-27 从 rc.30 合并的。上游发新版时：

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
| `router/web-router.go`、`middleware/cache.go`、`middleware/rate-limit.go` | 静态资源不限流、缺失 chunk 返回 404、immutable 缓存（见第六节） |
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
| `web/src/features/home/components/sections/hero.tsx` | 首页按钮：登录后显示「前往仪表盘 / 模型广场 / 文档」 |
| `web/src/lib/{chunk-load-error,stale-bundle}.ts`、`features/errors/general-error.tsx`、`i18n/config.ts`、`main.tsx`、`lib/http-client.ts`、`rsbuild.config.ts` | 前端自愈刷新、新版本提示、语言包懒加载（见第六节） |

rc.30 → rc.40 这次合并的经验：上游把重试判断从 `controller/relay.go` 挪到了 `service/relay_error.go`，重构了 `web/src/lib/http-client.ts` 和登录跳转 hook，`main.tsx` 里 `i18next` 的导入被上游删掉了（我们的 `ensureLocale(i18next.language)` 还要用，合并后要补回来）。解冲突时以上游为主体，把上表里的定制点重新加回去。

rc.40 → rc.41、rc.41 → upstream main（1a4166d8e）都是无冲突合并，上表定制点全部原样保留。

注意上游这 7 个提交里有一条：Waffo Pancake 的支付回调现在会校验 Store ID，后台「Waffo Pancake」设置里的 Store ID 必须已填写且和商户后台一致，否则订单回调会被拒绝、充值不到账。

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

