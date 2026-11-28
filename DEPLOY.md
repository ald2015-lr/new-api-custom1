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

对照 `deploy/docker-compose.yml`，确认环境变量和你现在跑的一致（尤其是 `SQL_DSN`；旧容器设置了 `SESSION_SECRET` 的，把同一个值写进 `deploy/.env`，否则切换后所有用户要重新登录一次，见第二十二节）。**旧容器的 `./data` 目录要一起带过来**——里面有上传的文件和本地缓存，虽然主数据在 MySQL，但丢了会导致图片/附件 404：

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

当前基线：`v1.0.0-rc.52`（2026-11-28 合并）。历次同步：rc.30 → rc.40（2026-09-27）→ rc.41（2026-10-01）→ rc.42 + 上游 `main` 到 `7aa3531ef` 的 1 个修复提交（2026-10-08）→ rc.46（2026-10-22）→ rc.49（2026-11-11）→ rc.51（2026-11-25）→ rc.52（2026-11-28），每次都以合并提交进入 `main`，保留上游的提交历史。上游发新版时：

```bash
git remote add upstream https://github.com/QuantumNous/new-api.git   # 只需一次
git fetch upstream --tags

git checkout main
git merge v1.0.0-rc.52        # 换成上游最新的 release tag，不要用 upstream/main
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
| `router/web-router.go`、`middleware/cache.go`、`middleware/rate-limit.go`、`middleware/frontend_static.go` | 静态资源不计入页面限流、缺失 chunk 返回 404、immutable 缓存与 304、页面/静态限流在 Redis 故障时放行（见第六节）；页面请求被限流时返回自动重试的提示页（`writeWebRateLimited`）；静态文件优先返回构建时生成的 `.br`，每种编码各有 ETag、可压缩文件的响应都带 `Vary`（见第二十节） |
| `service/relay_error.go`（rc.40 前在 `controller/relay.go`） | `DecideRelayRetry` 开头的"已向客户端输出就不再重试"守卫 |
| `controller/relay.go` | defer 里流已开始时改走 `helper.WriteStreamError` |
| `relay/helper/stream_scanner.go`、`stream_error.go`、`common.go` | 裸 `[DONE]`、流内错误事件、写超时续期 |
| `relay/channel/openai/relay-openai.go` | `thinking_to_content` 交错思考 |
| `relaykit/.../gemini_chat/to_oai_chat_resp.go` | thought 与正文分字段 |
| `common/constants.go`、`model/log_other.go`、`web/src/features/system-settings/maintenance/log-settings-section.tsx` | 「在使用日志中显示响应模型」开关（默认关；关闭时日志接口不返回 `response_model`，黄色「响应模型」标记对所有人隐藏） |
| `model/log.go`（`LogCountCap`、`countLogsUpToCap`、`boundLogPage`，`GetAllLogs` / `GetUserLogs` 多返回 `totalCapped`）、`controller/log.go`（`logPage`、`logPageQuery`）；前端 `web/src/components/data-table/hooks/use-data-table.ts`（`totalCountCapped`、`isPlaceholderData`，跳转完成前不重复调 `ensurePageInRange`）、`web/src/hooks/use-table-url-state.ts`（`ensurePageInRange` 用 `useCallback` 保持同一个函数，并返回是否跳转）、`core/pagination.tsx`、`web/src/tanstack-table.d.ts`（`TableMeta`，`ColumnMeta` 改用库自己的类型参数）、`features/usage-logs/components/usage-logs-table.tsx`（把 `total_capped` 和 `isPlaceholderData` 交给表格；同一文件还有第二十节第 9 条的加载失败重试和 `other` 只解析一次）、`web/src/routes/_authenticated/usage-logs/$section.tsx`（`pageSize` 最多 100） | 使用日志列表最多数 1 万条，超过时返回 `total_capped`、显示「10,000+」并可继续往后翻页（见第二十一节）；页码或每页条数小于 1 时按默认值，不会一次返回全部日志；上游改这两个列表函数时保留有上限的计数和每页条数；上游改这两个分页 hook 时保留「只跳一次」的写法，否则打开最后一页之后的页会卡住标签页 |
| `model/subscription.go`、`service/{billing_session,funding_source,quota,task_billing}.go`、`relay/common/relay_info.go`、`service/log_info_generate.go` | 结算超出订阅剩余额度时，订阅扣满、超出部分按套餐设置转扣钱包（见第九节）；订阅优先回退钱包：`user_subscriptions` 只按列更新（不再整行 `Save`），「是否允许回退钱包」看套餐当前设置而不是购买时的快照（见第九节） |
| 新文件 `setting/operation_setting/group_access_setting.go`、`model/group_access.go`、`service/group_access.go`、`middleware/group_access.go`，以及 `middleware/auth.go`、`middleware/distributor.go`、`service/group.go`、`controller/{group,pricing,token,option}.go`、`model/{option,user_cache}.go`、`constant/context_key.go`、`i18n/` 里的小钩子 | 分组充值门槛 + 白名单（见第八节） |
| `web/src/features/system-settings/billing/group-access-*`、`features/keys/*`、`features/pricing/*` | 分组充值门槛的后台设置页、令牌分组锁定显示、模型广场门槛标记 |
| `web/src/components/layout/components/public-header.tsx` | 手机端首页顶栏直接显示「模型广场」和公告铃铛；平板横屏后不再锁死页面滚动 |
| `model/topup.go` | 管理员补单：Creem 订单按额度原样入账（上游会放大 50 万倍）；重复补单不再给用户 0 记日志 |
| `middleware/task_plugin.go`、`relay/relay_task.go`、`relay/mjproxy_handler.go` | 基于旧任务的续作/放大请求也受分组准入限制 |
| 新文件 `model/channel_quota_limit.go`、`controller/channel_quota_limit.go`，以及 `model/{main,channel,channel_cache,ability}.go`、`service/channel_select.go`、`controller/relay.go`、`router/channel-router.go`、`i18n/` 里的小钩子；前端 `web/src/features/channels/*` | 渠道限额（见第十节）；新增 `channel_quota_limits` 表 |
| 新文件 `setting/operation_setting/group_billing_setting.go`、`service/free_group.go`，以及 `service/{billing,quota,task_billing}.go`、`controller/pricing.go`、`model/option.go` 里的小钩子；前端 `features/system-settings/billing`、`features/pricing`、`features/usage-logs` | 不扣费（免费）分组（见第十一节） |
| `model/model_pricing_conversion.go`（`ConvertAllModelPricing`）、`controller/model_pricing_config.go`、`router/api-router.go`、`pkg/billingexpr/fixed.go`、`model/pricing.go`；前端 `features/model-pricing`、`features/system-settings/models`、`features/pricing` | 一键把旧定价全部转为计费表达式；修复编辑器把占位表达式 `p * 0 + c * 0` 存成 $0 计费；按次表达式在模型广场归入「按次计费」（见第十二节） |
| 新文件 `relay/helper/keepalive_writer.go`，以及 `controller/relay.go`（安装保活写出器、出错时按已提交的格式写错误）、`relay/channel/api_request.go`、`relay/channel/providerplugin/stream.go`（rc.51 起）与 `relay/helper/stream_scanner.go`（装了写出器时跳过旧的 ping；`keepAliveWriterOf` 沿 `Unwrap` 穿过前面包着的 writer 找保活写出器）、`setting/operation_setting/general_setting.go`、`model/option.go`、`controller/plugin_protocol{,_image}.go`（插件同步任务）；前端 `features/system-settings/models/global-settings-card.tsx` | 长请求保活：流式按客户端协议发心跳（Claude 发 `event: ping`，Responses 在第一个事件之后发 `event: keepalive`，其余发 `: PING`）、非流式发前导换行，只发过心跳时仍可重试（见第十三节） |
| `relay/channel/api_request.go`（`DoApiRequest` / `DoFormRequest` 用 `http.NewRequestWithContext`）、`relay/channel/providerplugin/chain.go`（rc.51 起，provider 插件转换路由的 `sendStep` 同样用 `http.NewRequestWithContext(c.Request.Context(), …)`）、`service/relay_error.go`（`ClientGone`，`DecideRelayRetry` 的 `client_gone`）、`service/request_policy.go`、`controller/relay.go`（客户端已断开时不调用 `processChannelError`）、`relay/helper/stream_scanner.go`（读上游出错时先看客户端是否已断开）、`relay/responses_websocket.go`（WebSocket 连接上游失败时同样跳过 `ProcessChannelError`）、`relay/channel/{ollama/stream.go,coze/relay-coze.go,zhipu/relay-zhipu.go,cloudflare/relay_cloudflare.go,openai/audio.go}`（自己读上游的处理器：客户端断开后按已收到的文本结算、不记 ERR，TTS 非流式退款）；前端 `features/system-settings/request-policies/policy-label.ts` | 客户端断开时取消上游请求，不重试、不算渠道错误、退还预扣额度（见第十三节） |
| 新文件 `setting/operation_setting/model_square_setting.go`，以及 `controller/pricing.go`（`model_square` 字段）、`model/option.go`、`model/pricing_default.go`（按名称识别供应商的有序规则、供应商别名）、`controller/model_square_option.go`、`router/api-router.go` 与 `middleware/access_token_routes.go`（`PUT /api/option/model_square`）；前端 `features/pricing/lib/model-variants.ts`、`lib/model-name-fold.ts`、`lib/model-family.ts`、`lib/filters.ts`、`hooks/use-filters.ts`、`model-family*.tsx`、`model-variant-list.tsx`、`pricing-family-cells.tsx`、`components/pricing-sidebar.tsx`、`components/pricing-toolbar.tsx`、`features/system-settings/models/model-square-*`、`features/system-settings/api.ts` 与 `hooks/use-update-option.ts` | 模型广场按基础模型合并变体（见第十四节）；rc.51 起系列卡片、线路价格、分组价格和缓存价格列都先经上游的 `withBasePricing`，普通请求按模型价格计费的模型（`ordinary_pricing`）显示和单模型卡片一样的价格；合并版本号写法和带日期的快照、左侧筛选项开关、设置一次保存、按名称识别供应商（见第十四节）。rc.52 起上游也有一个同名的 `model_square_setting.go`（推荐排序 `sort_mode` / `sort_metric` / `sort_days` / `sort_models`），两边合成一个结构：显示设置照旧由 `IsModelSquareOptionKey` 和 `PUT /api/option/model_square` 处理，排序设置走上游的通用配置和 `PUT /api/option/`；`/api/pricing` 同时返回 `model_square` 和上游的 `model_order`，系列卡片由 `lib/model-family.ts` 的 `sortModelFamilies` 按 `model_order` 排序 |
| `pluginkit/host.go`（rc.52 起上游拆出的 `pluginkit` 模块） | `newAPIReservedKeys`（插件 key 不能使用的路径第一段）加了 `"ai"`：定制版的 NovelAI 原生接口 `/ai/generate-image` 等占用了 `/ai`。`router` 包的 `TestNewAPIReservedKeysMatchHostRoutes` 检查这个列表和全部路由的第一段一致，定制版以后新增顶层路由时同样要加进去 |
| `web/src/features/home/components/sections/hero.tsx` | 首页按钮：登录后显示「前往仪表盘 / 模型广场 / 文档」 |
| `web/src/lib/{chunk-load-error,stale-bundle}.ts`、`features/errors/general-error.tsx`、`i18n/config.ts`、`main.tsx`、`lib/http-client.ts`、`rsbuild.config.ts` | 前端自愈刷新、新版本提示、语言包懒加载（见第六节） |
| 新文件 `web/src/features/system-settings/*/section-meta.ts`（8 个设置页各一个）、`features/system-settings/utils/section-meta.ts`、`web/src/lib/build-version-meta.ts`，以及 `features/system-settings/utils/section-registry.ts`、`web/src/routes/_authenticated/system-settings/` 下的 15 个路由文件（`request-policies/index.tsx` 只做跳转，不引用注册表，没有改）、`components/layout/config/system-settings.config.ts`、`lib/build-metadata.ts`、`rsbuild.config.ts`（`splitChunks` 含 `console-kit` / `console-table` 两组、`runtimeChunk`、`html.meta`）、新文件 `web/scripts/console-chunks.{mjs,d.mts}` | 系统设置代码不进首屏（见第六节）：路由守卫和侧栏只引用各设置页的 `section-meta.ts`。上游给某个设置页的 `section-registry.tsx` 增删或调整分区时，同步改旁边的 `section-meta.ts`，`section-meta.test.tsx` 会检查两者的分区 id、顺序、默认分区和侧栏链接是否一致；路由文件或侧栏重新引用 `section-registry` 时测试会失败。构建版本号写在 `index.html` 的 `<meta name="new-api-version">` 里（写入前做 HTML 转义），不再编进 JS。控制台页面共用的界面组件和数据表格由 `console-kit`、`console-table` 两组单独成块，只在控制台的 chunk 之间共用：`scripts/console-chunks.mjs` 按 TanStack Router 代码分割的路由文件（`src/routes/_authenticated/` 下的 `?tsr-split=`）判断哪些 chunk 属于控制台，上游调整路由目录或代码分割方式时用 `scripts/__tests__/console-chunks.test.ts` 和构建结果确认这两个文件仍然生成 |
| 新文件 `web/src/lib/route-chunk-prefetch.ts`、`web/src/features/auth/hooks/use-post-login-prefetch.ts`，以及 `features/auth/{sign-in,sign-up}/index.tsx`、`features/auth/lib/auth-redirect.ts`（`DEFAULT_AUTH_REDIRECT`）、`features/auth/hooks/use-auth-redirect.ts`、`web/src/routes/(auth)/sign-in.tsx` | 登录页和注册页空闲时预取登录后要进入的页面的代码（见第二十节第 9 条）：用路由器的 `loadRouteChunk` 只下载这些页面的 chunk，不运行路由的 `beforeLoad` 和 `loader`，不发接口请求。上游改登录页、注册页或登录跳转 hook 时保留 `usePostLoginPrefetch`；不要换成 `router.preloadRoute` 或悬停预加载，它们会运行守卫和 loader（会话检查、接口请求）。`src/lib/__tests__/route-chunk-prefetch.test.ts` 和 `src/features/auth/hooks/__tests__/use-post-login-prefetch.test.tsx` 覆盖；只做跳转的分区首页（如默认进入的 `/dashboard`）靠旁边的 `$section` 路由找到要预取的页面，上游给数据看板的路由改名或给 `dashboard/index.tsx` 加了页面时，前一个测试（或类型检查）会失败，这时按新的路由调整 `route-chunk-prefetch.ts` 和这个测试 |
| `.github/workflows/ci.yml` | 前端作业的 `timeout-minutes` 从上游的 10 放宽到 20：rc.51 起前端测试从 243 个文件增加到 274 个、耗时多约 19%，较慢的 GitHub runner 上 10 分钟跑不完 |
| `middleware/rate-limit.go`（`AuthRefreshRateLimit`、Redis 出错时的内存回退与限频日志）、`common/{constants,init}.go`、`router/api-router.go`（刷新路由）、`common/log_text.zh-CN.json` | 刷新登录状态单独限流 `AUTH_REFRESH_RATE_LIMIT*`；Redis 出错时 API 与按用户限流改用内存计数 `RATE_LIMIT_REDIS_FAILURE`（见第六节、第二十节） |
| `model/utils.go`（`FlushBatchUpdates`、`batchFlushMu`）、`main.go`（`srv.Shutdown` 之后调用） | 停机前写入批量更新中尚未落库的额度变化（见第二十节第 5 条） |
| `model/log.go`（`createLog` 用 `SkipDefaultTransaction`） | 写使用日志不再包默认事务，每个中转请求少 3 次数据库往返 |
| `model/channel_quota_limit.go`（`channelQuotaLimits` 改为原子快照 + 单个后台刷新 + 刷新期间本地写入保留） | 渠道限额快照不再在选渠道路径上同步查库 |
| `service/rankings.go`（`rankingBuilds` singleflight、`buildRankings` 包变量）；rc.52 起还有 `service/model_square_order.go`（`modelUsageLoads` singleflight、`loadModelUsageTotals` 包变量） | 排行榜同一时间只构建一次，过期缓存先返回旧数据再后台重建；模型广场排序用的用量（公开的 `/api/pricing` 每次访问都要）同样处理 |
| 新文件 `model/enabled_models_cache.go`，以及 `model/channel_cache.go`（重载时建快照）、`model/model_pricing_config.go`（`GetModelPricingIssues`）、`service/group.go`（`GetGroupsEnabledModelsCached`）、`controller/user.go`（`GetUserModels`） | 可用模型列表与定价问题检查在开启内存缓存时读渠道缓存；`/v1/models` 等其他调用不变 |
| `web/index.html`（启动外壳、看门狗脚本、noscript）、`web/rsbuild.config.ts`（`html.tags` 把应用样式表放到 body 末尾、启动外壳之后）、`web/src/main.tsx`、新文件 `web/src/lib/boot.ts`、`web/src/routes/__root.tsx`、`web/src/lib/status-query.ts`、`web/src/features/setup/api.ts`、`web/src/features/home/hooks/use-home-page-content.ts`、`main.go`（`InjectUmamiAnalytics` 注入的统计脚本用 `async`） | 首屏不再白屏、老浏览器提示、语言包最多等 10 秒、setup 从 `/api/status` 判断、首页内容先用缓存、umami 统计脚本不再挡住页面启动（见第二十节） |
| `web/src/lib/auth-session.ts`、`web/src/routes/_authenticated/route.tsx`、`web/src/features/errors/general-error.tsx`、`web/src/lib/server-error-message.ts`、`web/src/lib/query-client.ts`、`web/src/features/usage-logs/components/usage-logs-table.tsx`（加载失败时显示原因和「重试」；同一文件还有第二十一节的「10000+」）和 `usage-logs-mobile-card.tsx`（手机上的「重试」按钮 `emptyAction`）；带 `meta: { errorPage: false }` 的可选查询：`web/src/lib/status-query.ts`（状态）、`web/src/hooks/use-notifications.ts`（公告）、`web/src/features/dashboard/components/overview/performance-health-panel.tsx` 和 `dashboard/components/models/performance-overview.tsx`（性能面板）、`web/src/features/system-settings/models/pricing-issues.ts`（定价问题角标）、`usage-logs-table.tsx` 里决定是否显示计费来源列的套餐查询、`web/src/features/pricing/components/{model-card-grid,model-details,model-details-performance}.tsx`（模型广场的性能数据） | 刷新临时失败时不误退出登录、429 本地化提示、可选查询失败不跳错误页、请求重试策略、日志表加载失败可重试（日志接口直接返回 HTTP 500 时照旧跳错误页，见第二十节第 9 条）。上游改这些可选查询时保留 `errorPage: false`；标记丢了 `src/lib/__tests__/query-client.test.ts`、`src/lib/__tests__/optional-query-error-page.test.tsx`、`features/pricing/__tests__/perf-metrics-error-page.test.tsx` 或 `features/usage-logs/__tests__/logs-error-state.test.tsx` 会失败 |
| 新文件 `web/src/lib/{idle-prefetch,lazy-with-retry}.ts`、`web/src/hooks/use-intent-prefetch.ts`、`web/src/components/{lazy-chunk-boundary,lazy-dialog}.tsx`、`web/src/test-utils/static-import-graph.ts`；模型广场 `web/src/features/pricing/{index.tsx,api.ts}`、`hooks/{use-pricing-data,use-pricing-access-redirect}.ts`、`lib/{model-details-chunks,model-details-tab-chunks}.ts`、`components/index.ts`（不再导出详情组件）、`components/{model-details,model-details-api,model-details-performance}.tsx` 和新文件 `components/{model-details-drawer,model-details-lazy,model-details-code-sample,model-details-skeleton,pricing-load-error}.tsx`、`web/src/lib/{nav-modules,status-query}.ts`（`getModuleAccessForPageGuard`、`fetchStatusForGuard`）、`web/src/routes/pricing/`；渠道列表 `web/src/features/channels/components/{channels-dialogs,channels-columns,channels-primary-buttons,data-table-row-actions}.tsx`、`features/channels/index.tsx`、新文件 `features/channels/lib/channel-dialog-chunks.ts` | 模型广场的详情抽屉、性能图表（VChart）和代码示例（CodeMirror）按需加载；价格加载失败时显示原因和「重试」，不再显示 0 个模型（价格查询 `errorPage: false`）；模型广场的守卫最多等 `/api/status` 3 秒，拿不到时按状态快照或默认值决定，不再把访客送回首页；未登录访客的 `/api/pricing` 返回 401/403 时直接转到登录页或首页。渠道列表的弹窗和抽屉第一次打开时才下载；这些部分用 `lazyWithRetry` 而不是 `React.lazy` 创建，一次加载失败后再次打开（或点提示里的「重试」）会重新下载，上游新增这类按需加载的部分时同样用它。已登录访客的 401 不跳登录页，留在页面上显示加载失败和「重试」。详情标签页和数据看板分区标签在指针或焦点停留 200 ms 后才预取，省流量模式下不预取。上游改 `channels-dialogs.tsx`、`channels-columns.tsx` 或模型详情时保留按需加载：`src/features/channels/components/__tests__/channels-chunks.test.ts` 和 `src/features/pricing/__tests__/model-details-chunks.test.ts` 会在这些模块又被直接导入时失败 |
| `web/src/features/{channels,models,subscriptions,dashboard,usage-logs}/api.ts`、`features/{keys,redemption-codes,users}/api.ts`（删除接口去掉结尾斜杠），守卫测试 `src/lib/__tests__/api-collection-paths.test.ts` | 列表接口带结尾斜杠，省掉 301/307 跳转；删除令牌、兑换码、用户时不再多一次 307。上游新增或改了这些接口的路径时跑一下守卫测试 |
| 新文件 `web/scripts/slim-locale.mjs`（及 `.d.mts`）、`web/rsbuild.config.ts`（只在生产构建时加的内联插件） | 生产构建的英文语言包去掉键和值相同的条目，`en.json` 源文件和开发模式不变；`src/i18n/__tests__/slim-en.test.ts` 检查 7 种语言的翻译结果不变。上游改了 `rsbuild.config.ts` 时保留这个插件 |
| 新文件 `web/src/components/lazy-markdown.tsx`、`web/src/components/markdown-loader.ts`，以及 `components/rich-content.tsx`、`components/notification-popover.tsx`、`features/dashboard/components/overview/faq-panel.tsx`、`features/home/hooks/use-home-page-content.ts` | KaTeX/marked 只在显示 Markdown 时才下载（铃铛在悬停、聚焦或按下时提前加载，首页内容是 Markdown 时也提前加载）；分块加载失败先自动刷新一次，其他错误按纯文本显示 |
| 新文件 `web/src/components/react-icon-presets.ts`、`components/react-icon-by-name.tsx` | 支付方式的四个预置图标（支付宝、微信、Stripe、信用卡）用 react-icons 自带的图标数据直接生成，钱包页不再下载整个 `si`、`lu` 图标包；其他图标名照旧按需加载整包。升级 react-icons 后跑 `src/components/__tests__/react-icon-by-name.test.tsx`（逐个对比图标包里的同名组件） |
| `web/src/lib/motion.ts` | 页面切换动画去掉整页 `blur()` 滤镜，只保留淡入和小位移 |
| `web/src/features/usage-logs/lib/format.ts`（`getLogOther`），以及日志列、手机卡片、详情弹窗和 `usage-logs-table.tsx` | 使用日志每行的 `other` 只解析一次 |
| 新文件 `web/src/features/dashboard/lib/section-chunks.ts`，以及 `features/dashboard/index.tsx`、`features/dashboard/components/overview/overview-dashboard.tsx`、`components/layout/types.ts`（`NavLink.prefetch`）、`components/layout/components/nav-group.tsx`、`hooks/use-sidebar-data.ts` | 数据看板各分区的分块在悬停或聚焦侧栏「数据看板」和分区标签时预取，概览页打开 2 秒后空闲时也预取图表（省流量模式和 2G 网络不预取，没有权限的分区不预取） |
| 新文件 `web/scripts/precompress.mjs`（及 `.d.mts`）、`web/package.json`（`build` 改为 `rsbuild build && bun scripts/precompress.mjs`）、`web/vitest.config.ts`、`web/tsconfig.node.json`（测试和类型检查包含 `scripts/__tests__`） | 前端构建结束时给 `dist/static` 下大于 1 KB 的 JS、CSS、SVG、JSON 生成 brotli（质量 11）的 `<文件>.br`，后端对接受 brotli 的浏览器优先返回它（见第六节）。只用 `node:zlib`，Dockerfile 不变；构建多约 30 秒，`dist` 和二进制、镜像各大约 16 MB。上游改了 `build` 脚本时保留后半段 |
| 新文件 `common/session_secret.go`、`model/session_secret.go`（`server_secrets` 表、`InitSessionSecret`），以及 `common/init.go`（`InitEnv` 记下密钥是否来自 `SESSION_SECRET` / `CRYPTO_SECRET`）、`main.go`（`model.InitOptionMap()` 之后调用 `model.InitSessionSecret()`）、`model/main.go`（`AutoMigrate` 加 `ServerSecret`）、`model/option.go`（`KeepLoginAcrossRestartEnabled` 默认值与校验，`KeepLoginAcrossRestartSource` 只读）、`controller/option.go`（`GET /api/option` 返回 `KeepLoginAcrossRestartSource`）、`relay/common/safety_identifier.go`（「标识重启后会变」的提示改看 `common.CryptoSecretSurvivesRestart()`）、`controller/auth_guard.go`（审计「输入内容指纹」的密钥改用 `common.AuditFingerprintSecret()`）、`common/log_text.zh-CN.json`、`i18n/i18n_test.go`，测试 `model/session_secret_test.go`、`service/auth_session_test.go`、`controller/auth_session_test.go`、`controller/auth_guard_test.go`（`TestAuditFingerprintIsNotKeyedWithTheKeptLoginKey`） | 重启或更新后保持用户登录（见第二十二节），新增 `server_secrets` 表。上游调整 `InitResources` 的启动顺序时，`InitSessionSecret` 要留在第一个读 `common.SessionSecret` / `common.CryptoSecret` 的地方之前（现在是 `InitRedisClient` 和路由）；上游新增读这两个变量、或在 `init()` / 包变量里用它们算值的代码时，确认它不会在这之前运行。`server_secrets` 不能并进 `options` 表。`CryptoSecret` 可能就是数据库里保存的那把密钥：对用户输入的内容（可能是密码）算指纹再存进数据库或日志的代码要用 `common.AuditFingerprintSecret()` |
| 新文件 `web/src/features/system-settings/auth/keep-login-field.tsx`，以及 `auth/basic-auth-section.tsx`（把它放在第一项）、`auth/section-registry.tsx`、`auth/index.tsx`、`features/system-settings/types.ts`、`features/channels/components/channel-moderation-settings.tsx`（安全标识说明），测试 `auth/__tests__/keep-login-settings.test.tsx`、`auth/__tests__/registration-abuse-settings.test.tsx` | 「重启或更新后保持用户登录」开关、`SESSION_SECRET` 覆盖提示和「下次重启生效」提示（见第二十二节）。上游改基本身份验证设置页时保留这一项 |

rc.30 → rc.40 这次合并的经验：上游把重试判断从 `controller/relay.go` 挪到了 `service/relay_error.go`，重构了 `web/src/lib/http-client.ts` 和登录跳转 hook，`main.tsx` 里 `i18next` 的导入被上游删掉了（我们的 `ensureLocale(i18next.language)` 还要用，合并后要补回来）。解冲突时以上游为主体，把上表里的定制点重新加回去。

rc.40 → rc.41、rc.41 → upstream main（1a4166d8e）都是无冲突合并，上表定制点全部原样保留。

rc.41 → rc.42（2026-10-08）：上游自己也修了「前端静态文件把页面限流用光」——静态文件改为预压缩直出，并单独加了一个默认关闭的静态限流（`GLOBAL_STATIC_RATE_LIMIT_ENABLE` / `GLOBAL_STATIC_RATE_LIMIT` / `GLOBAL_STATIC_RATE_LIMIT_DURATION`）。冲突在 `router/web-router.go`、`middleware/rate-limit.go`：以上游结构为主，保留我们的缺失 chunk 404、Redis 故障放行，并在预压缩路径补上 `If-None-Match` → 304（上游这条路径不回 304，会让浏览器每次重新下载整个 chunk）。另外上游把 Go 升到 1.26、构建时用了 moejs 的 PGO 文件，Dockerfile 已同步，不需要手动处理。

注意 rc.41 → upstream main（1a4166d8e）那次合并的 7 个提交里有一条（feefe09f2）：Waffo Pancake 的支付回调现在会校验 Store ID，后台「Waffo Pancake」设置里的 Store ID 必须已填写且和商户后台一致，否则订单回调会被拒绝、充值不到账。

rc.42 → rc.46、rc.46 → rc.49 两次的冲突和处理写在合并提交 `f2184ca4d`、`11a3b1251` 的提交信息里（`git show -s <提交>`）。

rc.49 → rc.51（2026-11-25，合并提交 `4cdc46af5`）：上游这一版的主体是 provider 插件、请求处理插件和社区插件市场（#8271）。57 个文件冲突（后端 38、前端 19），绝大多数是两边在同一位置各加了一段，两边都保留：
- 中继入口（`controller/relay.go`、`controller/plugin_protocol*.go`、`middleware/task_plugin.go`、`relay/responses_websocket.go`、`relay/relay_task.go`、`relay/{claude,gemini,image}_handler.go`、`relay/responses_request.go`、`relay/chat_completions_via_responses.go`）：上游请求插件的各个阶段和我们的外部审查、`safety_identifier`、保活、上游余额错误屏蔽并存。顺序是：全局实例和请求所在分组的实例的插件 ingress（可改写请求体）→ 解析 → 每次尝试先审查 → 设置 `safety_identifier` → 渠道绑定实例的 ingress（以及自动分组跨分组重试后才生效的分组实例的 ingress）→ 插件 outbound → 参数覆盖 → 插件 egress（Chat Completions 转 Responses 时，渠道绑定实例的 ingress、outbound 和参数覆盖都在设置标识之前，见第十八节限制 12）。审查只看得到请求开始时那次 ingress 的改写，看不到渠道绑定实例的 ingress 和 outbound 的改写（和参数覆盖一样，见第十八节限制 15；视频任务 `/v1/videos` 例外，每次尝试先执行这些 ingress 再审查）；请求处理插件只有 root 能配置。
- 渠道余额：上游把「更新全部渠道余额」改成并发的系统任务，每次查询结果存进 `other_info.balance_detail`。NovelAI 的规则（余额为 0 不自动禁用、币种是 Anlas）挪进了上游的新文件 `controller/channel_balance.go`；渠道类型在 NovelAI 和其他类型之间切换时，`ClearBalance` 连 `balance_detail` 一起清掉。
- `service/relay_error.go` 用上游抽出的 `RecordRelayErrorLog`，上游余额错误屏蔽合在同一个内部函数里；`service/quota.go` 的 `PreWssConsumeQuota` 用上游的新返回值，免费分组照旧不扣费；NovelAI 的 `n_samples` 校验挪到参数覆盖和插件 outbound 都执行完之后，两者任一改了请求体都会再校验。
- NovelAI 原生接口（`/ai/generate-image` 及 `/v1`、流式变体，`relay/novelai_handler.go`）原样转发客户端请求体，请求处理插件按透传渠道执行：每次尝试的 egress 检查原样发出的 JSON（全局实例和渠道绑定的实例都执行，拒绝时不发送、不计费），渠道绑定插件的 ingress 和 outbound 不执行，在日志里记为未检查（`router` 包 `TestNovelAINativeRoutesRunTheRequestPluginEgress` 覆盖）。
- 上游去掉了「敏感词过滤」设置（`CheckSensitive*`、`SensitiveWords` 等）：启动时把原来的敏感词自动搬进一个全局「请求过滤」插件实例，并删除旧设置项；回滚到旧版本后旧的敏感词设置不会回来（旧版本里敏感词检查变成关闭、词表为空）。
- 保活：上游 provider 插件的流式转换器会自己起 ping，请求插件的响应采集和回复检查会包在保活写出器外面，合并后两路心跳会同时写，只发过心跳的失败无法换渠道重试。现在 provider 插件路径装了保活写出器时不再自起 ping，`keepAliveWriterOf` 沿 `Unwrap` 找保活写出器（`router` 包 `TestPluginStreamErrors` 和 `TestChannelReportPluginKeepsKeepAliveRetries` 覆盖）。
- i18n：后端 `i18n/keys.go` 和三个 yaml、前端 7 个语言包都按 key 取并集，合并后 `node scripts/sync-i18n.mjs` 无差异。

下次合并要注意：同步分支经 PR 合入 `main` 时选「Create a merge commit」，不要 Squash，否则丢掉上游父提交，下一次同步会把上游整段历史都当成冲突；上游新增的管理接口要同时出现在 `middleware/access_token_routes.go` 里（`router` 包的 `TestAccessTokenRouteRulesCoverEveryDashboardRoute` 会检查），并确认副管理员拿不到充值数据（第十七节）；上游每多一个能改写请求体的插件阶段，都要确认它在审查之前还是之后（第十八节），以及 NovelAI `n_samples` 这类计费数量在改写之后仍被校验；上游新加的 ping，或新包在 `c.Writer` 外面的 writer，都要确认装了保活写出器时不会再发第二路心跳（writer 要实现 `Unwrap`）；上游新增的、不经过 `DoApiRequest` 自己构建上游请求的代码，都要用 `http.NewRequestWithContext(c.Request.Context(), …)` 绑定客户端的请求，否则客户端断开后上游照样把回答生成完，还可能照常计费（provider 插件路由由 `router` 包 `TestPluginConverterCancelsTheUpstreamWhenTheClientLeaves` 覆盖）。

rc.51 → rc.52（2026-11-28，合并提交 `0ab02e0fc`）：上游这一版有一个找回密码的安全修复，插件 API 拆成了独立的 `pluginkit` 模块，provider 插件路由可以按渠道开关，模型广场加了推荐排序。32 个文件冲突（后端 19、前端 13）：
- 找回密码与登录（`common/verification.go`、`controller/misc.go`、`model/user.go`）：上游的修复（重置令牌和链接都用账号里存的邮箱、邮件发到这个地址、令牌由 `ConsumeCodeWithKey` 校验时即作废、登录时精确匹配邮箱）和第十九节的验证码、每邮箱限额、审计、登录锁定并存。定制版的登录走 `FindPasswordLoginUser`，上游的精确邮箱查找放进了这里，只改 `ValidateAndFill` 对实际登录不起作用。
- 模型广场：两边都新建了 `setting/operation_setting/model_square_setting.go`，类型、变量、配置名和校验函数都同名，合成一个结构（见上表）。上游新的默认排序「推荐」在没设推荐顺序时和原来的按名称排序一样，系列卡片的排法见第十四节。
- 选渠道：上游重构了 `CacheGetRandomSatisfiedChannel`，模型不在任何可用分组时返回 404、否则 503；定制的已试渠道和渠道限额用完的 429 都保留，429 在上游的判断之前。`ShouldDisableChannel`、`RecordPolicyFailure` 改用上游的 `RelayErrorSource`，NovelAI / Gemini 免费 key 忙碌仍不禁用渠道，客户端断开仍单独归类。
- 任务插件和外部审查：上游 #8335 去掉了「一种内置渠道类型只能由一个插件声明」的限制，Sora 以外的任务插件也能在 OpenAI 渠道上提交任务，而审查只认得 Sora 的请求（`relay/moderation` 的 `CheckTask` 只看 `prompt`）。现在开启审查的 OpenAI 渠道上，其他任务插件的提交一律按「不支持」拒绝（第十八节限制 7）；`relay/relay_task.go` 把执行插件的 key（新文件 `relay/channel/task/jsplugin/plugin_key.go`）传给 `CheckTask`。
- relaykit 的 Gemini 转换：思考和正文照旧分字段，同时用上游的逐字拼接文本和 grounding 偏移（只按正文计算）。
- 上游 #8474 让 Claude 格式的每个事件只以一个空行结尾，定制版的流内错误事件（`WriteStreamError`）跟着变。上游新的 Gemini 音频计费测试假设流里的 `{"error":…}` 被丢掉、照常计费；定制版按上游给的状态码报错（第七节），和 Claude 流内的 `error` 事件一样退还预扣额度，出错前已输出的部分不计费，这个测试改成期望报错。
- i18n：前端 7 个语言包按 key 取并集，没有两边都改过的 key，`node scripts/sync-i18n.mjs` 无差异。

下次合并还要注意：上游再改找回密码或登录时，确认改动落在定制版实际调用的 `FindPasswordLoginUser` 和 `SendPasswordResetEmail` 的分支里，每个请求仍写一条审计、未注册的邮箱仍得到同样的回答；上游的测试连续给同一邮箱发重置邮件时，要在测试里关掉定制的找回密码验证码和每邮箱限额（`controller/email_delivery_test.go` 就是这样做的）；上游给 `model_square_setting.go` 加设置项时，分清它属于显示设置还是排序设置，两者各走各的保存接口；上游改模型广场的排序时，同时看 `lib/model-family.ts` 里系列卡片的排序；定制版或上游新增顶层路由时，同步 `pluginkit/host.go` 的 `newAPIReservedKeys`；上游给 Sora 以外的内置任务插件加上 OpenAI 渠道类型（1）时，开启审查的渠道会拒绝它的任务，要支持就先在 `relay/moderation` 里为它的请求写文字提取。

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
| Redis 抖动 | 限流中间件直接回 500，每个资源请求都变 500 | 页面层限流在 Redis 出错时放行并记日志；API 层和按用户的限流在 Redis 出错时临时改用本机内存计数继续限流（超限照常 429 并带 Retry-After），不再返回 500，日志每个限流器最多 10 秒一条（「限流检查失败，改用内存限流」）；多节点时每个节点各自计数，相当于暂时放宽到节点数倍。`RATE_LIMIT_REDIS_FAILURE=reject` 恢复旧行为（直接返回 500）。限流次数或窗口配成 0 或负数属于配置错误，不按 Redis 故障处理，照旧直接返回 500 |

首屏慢的处理：

- `/static/*` 文件名带内容 hash，现在按 `public, max-age=31536000, immutable` 缓存，并带 ETag，回源验证得到 304 而不是重新下载
- 七种语言包原本全部打进首屏同步 chunk（约 2.5 MB 原始 / 770 KB gzip），现在只内置英文，当前语言按需加载
- 登录态刷新请求加了 30 秒超时，卡住时以临时错误结束而不是让首屏一直等（普通 API 客户端不加，渠道测试等长请求不受影响）
- 每个响应都带 `X-New-Api-Version`；前端发现与自身构建版本不一致时弹一次「有新版本，请刷新」提示，避免用户在旧页面里一路点到已失效的 chunk
- 系统设置的代码不再打进首屏入口：路由守卫和侧栏只用各设置页的分区元数据，设置页本身随各自页面按需下载。所有页面首次打开都要下载的 JS/CSS 从约 812 KB 降到约 292 KB（6 个文件，gzip，不含语言包）
- chunk 加载器和全部 chunk 文件名的对照表单独放在 `runtime.*.js`，构建版本号写进 `index.html` 的 `<meta name="new-api-version">`（`index.html` 不缓存）：发布只改了某个页面的新版本后，老用户只需重新下载约 19 KB 的 runtime 和改动的那个页面（新增了界面文字时还有当前语言的语言包），不再重新下载约 317 KB 的入口文件；改的是各页共用的界面组件或数据表格时，控制台页面另外重新下载一次 `console-kit`（约 141 KB）或 `console-table`（约 28 KB），模型广场、登录页等公开页面重新下载各自带这段代码的页面文件（约 20–80 KB）；登录页和注册页的空闲预取（见下一条）在页面显示之后同样会重新下载改过的 `console-kit` 或 `console-table`（后者只在 `redirect` 指向带数据表格的页面时），例如改了 `components/ui/select.tsx` 的一句文字后，登录页一共重新下载约 0.18 MB、注册页约 0.16 MB。新版本提示照常按这个版本号和服务器的 `X-New-Api-Version` 比较
- 控制台各页共用的界面组件（`components/ui`、@base-ui、表单）和数据表格分别放在 `console-kit`（约 141 KB）和 `console-table`（约 28 KB）两个文件里，第一次打开控制台时下载，之后切换页面主要只下载该页自己的代码；首页和模型广场不下载这两个文件；登录页和注册页在页面显示 2 秒后、浏览器空闲时提前下载登录后要进入的页面的代码（含 `console-kit`，见第二十节第 9 条）。其余低于拆分阈值的公共代码仍会复制进用到它的页面文件。浏览器实测（2 Mbit/s 网络，下面是实际传输的 brotli 字节）：直接打开登录页时，登录页本身约 0.49 MB（旧版本约 0.84 MB，控制台的共用代码就在它的入口文件里），显示 2 秒后再提前下载约 0.29 MB 的数据看板代码，登录后进入数据看板不用再下载代码（旧版本约 66 KB）；已登录的管理员直接打开数据看板，再依次进入使用日志、令牌、渠道、用户、模型管理、个人设置，一共下载约 1.69 MB（旧版本约 1.82 MB），其中这 6 次切换约 0.52 MB（旧版本约 0.49 MB）。第一次进入使用日志要下载 `console-table` 和抽屉组件，第一次进入模型管理要下载约 80 KB 的模型定价编辑器（旧版本它们都在入口文件里），这两步分别比旧版本多等约 0.15 秒和 0.35 秒；渠道列表快约 0.9 秒，令牌、用户、个人设置相差不到 0.05 秒。控制台各页首次打开约 13–18 个 JS/CSS 文件（不算按数据加载的供应商图标小文件；数据看板的 18 个里有 4 个是页面显示之后的空闲预取），同一批最多约 10 个，网站还没开 HTTP/2 时也不会因为文件太碎多排几轮
- 模型广场的详情抽屉、性能图表（VChart）和代码示例（CodeMirror），以及渠道列表的弹窗和抽屉，都在第一次打开时才下载；详情抽屉和渠道编辑器在悬停、聚焦按钮或页面空闲时提前下载。加上入口变小，模型广场首次打开要下载的 JS/CSS 从约 1.87 MB 降到约 0.87 MB，渠道列表从约 1.42 MB 降到约 0.91 MB（gzip；渠道列表空闲时再提前下载约 0.11 MB 的渠道编辑器）
- 构建产物里有 `.br`（前端构建时生成）的静态文件，对接受 brotli 的浏览器直接返回 `.br`，比 gzip 再小约 20%；每种编码各有自己的 ETag（`"<路径>"`、`"<路径>-gzip"`、`"<路径>-br"`），可压缩文件的所有响应都带 `Vary: Accept-Encoding`，旧版本发出的 ETag 仍然能得到 304
- 打开页面被每 IP 页面限流拦下时，浏览器不再显示空白错误页，而是「访问过于频繁，N 秒后自动重试」的中英文提示页，N 秒（最多 60 秒）后自动刷新；脚本、样式、接口请求仍然是空的 429

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
# 接受 brotli 时应为 content-encoding: br，并带 vary: Accept-Encoding
curl -sI -H 'Accept-Encoding: gzip, br' https://<你的域名>/static/js/index.$(hash).js | grep -iE "^(content-encoding|vary|etag)"
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

**效果：** 模型广场里同一个基础模型只显示一张卡片。比如 `[按次]claude-opus-4-6`、`[按次]claude-opus-4-6-thinking-high`、`[正价kiro]claude-opus-4-6-thinking-max` 都归到 `claude-opus-4-6` 这张卡。下面两种写法也并到同一张卡：
- **版本号写法不同**：`claude-opus-4-5` 和 `claude-opus-4.5`、`claude-3-5-sonnet` 和 `claude-3.5-sonnet`、`doubao-1-5-pro` 和 `doubao-1.5-pro` 是同一个模型；大小写不同（`DeepSeek-V3` / `deepseek-v3`）、名称前多一段 `厂商/`（`deepseek-ai/DeepSeek-V3`、`hf:deepseek-ai/DeepSeek-R1-0528`）也算同一个。`glm-4-9b`、`llama-3-8b` 这类参数量、日期、`gpt-4-32k` 这类上下文长度不受影响。名称和版本号之间的 `-`（`llama-3.3-70b-instruct` 与 `llama3.3-70b-instruct`、`qwen3-32b` 与 `qwen-3-32b`）不算同一种写法，仍是两张卡。
- **带日期的快照**：`gpt-4o-mini-2024-07-18` 并到 `gpt-4o-mini`，`tts-1-1106` 并到 `tts-1`，`claude-opus-4-5-20251101` 并到 `claude-opus-4-5`，`[按次]deepseek-v4-flash-0731` 并到 `deepseek-v4-flash`（线路「按次」、变体 `0731`），`kimi-k2-thinking-251104` 并到 `kimi-k2`（变体 `thinking · 251104`）。日期原样作为变体名，排在 thinking 等具名变体之后，新的在前。

展示方式：
- 卡片正面：标题、供应商、其他写法（「也写作 …」）、各线路（名称开头的 `[...]` 标签）及其最低价、变体标签、综合状态条；
- 点「展开」：卡片所在行的下方展开一个整行面板，按线路分组列出每个变体，每行显示变体名和**完整模型名**，以及价格、状态条、延迟、吞吐；「复制」复制的是完整模型名，「详情」打开原来的模型详情；
- 表格视图同样按家族一行、点 ▸ 展开；只有一个变体的模型照常显示；
- 搜索时输入任一写法都能找到整张卡：搜 `claude-opus-4-5` 也会列出 `claude-opus-4.5` 的成员，搜 `claude-opus-4.5` 或 `claude-opus-4.5-2025` 能找到 `claude-opus-4-5-20251101`，带线路标签搜 `[正价kiro]claude-opus-4-5` 能找到 `[正价kiro]claude-opus-4.5`。

**只影响展示**：用户调用、计费、日志里用的模型名一律不变。

**卡片标题：** 用成员里最多的那种写法（去掉线路标签、变体后缀和日期后），例如 3 个成员写 `claude-opus-4.5`、2 个写 `claude-opus-4-5` 时标题是 `claude-opus-4.5`，另一种写法显示为「也写作 claude-opus-4-5」（只列出确实有成员叫这个名字的写法）。数量相同时依次看：不带线路标签的模型用的写法；不带 `厂商/` 前缀的；厂商自己 API 的写法（Anthropic、字节跳动、NovelAI、快手、Mistral 以及同步来的「Volcengine Ark」「Volcengine Ark Coding Plan」用 `-`，其他厂商用 `.`）；全小写的；字母顺序。没有任何成员正好叫这个名字时（比如只有 `doubao-seed-1-6-250615` 和 `doubao-seed-1-6-251015`），标题用最新快照的完整名称，它的变体标签排在最前。标题按模型广场里这一组的全部成员来定，搜索或筛选时卡片不会改名。卡片的供应商是标题模型的供应商（筛选后标题模型不在结果里时，取显示出来的第一个成员），它没有供应商时取显示出来的成员里最多的那个。

**哪些日期会并入：** 只看名称末尾（变体后缀去掉之后），每个名称最多一个日期，另外可以再带一个 Vertex 的 `@001` 这类版本号：

| 写法 | 例子 |
|---|---|
| `-YYYY-MM-DD` | `gpt-4o-mini-2024-07-18`、`qwen-plus-2025-07-28` |
| `-YYYYMMDD`、Vertex 的 `@YYYYMMDD` | `claude-opus-4-5-20251101`、`claude-opus-4-5@20251101`、`hunyuan-2.0-thinking-20251109` |
| `-YYMMDD` | `doubao-seed-1-6-250615`、`glm-4-flash-250414`、`kimi-k2-thinking-251104` |
| `-MMDD` | `tts-1-1106`、`deepseek-v4-flash-0731`、`grok-4-0709` |
| `-YYMM` | `mistral-large-2411`、`qwen3-235b-a22b-instruct-2507` |
| `-MM-DD`、`-MM-YYYY`、`-YYYY-MM`、`-YYYYMM` | `gemini-2.5-pro-preview-05-06`、`command-r-08-2024`、`jamba-large-1.7-2025-07`、`deepseek-v4-flash-202605` |
| Vertex 的 `@2411`、`@2411-001`、`@001` | `codestral@2405`、`mistral-large@2411-001`、`text-bison@002`；`mistral-small-2503@001` 显示为「2503 · 001」 |
| Gemini / Imagen / Veo 的 `-001`、`-002` 版本 | `gemini-2.0-flash-001`、`imagen-4.0-generate-001` |
| `-latest` | 只有同页已有这个模型、并且它带版本号或有带日期的快照时才并入（`claude-3-5-sonnet-latest`、`qwen-plus-latest`）；`gemini-pro-latest`、`chatgpt-4o-latest`、`claude-opus-latest`、`chat-latest` 不并 |

**不会并入的：** `text-embedding-ada-002`、`text-embedding-004`、`davinci-002`、`lyria-002` 这类 `-00N` 是模型名的一部分；日期不在末尾的（`gpt-4-1106-preview` 是 GPT-4 Turbo 预览版，不是 `gpt-4` 的快照；`kimi-k2-0905-preview`、`o3-mini-2025-01-31-high`、`qwen-plus-2025-12-01-us`）；最后一段带 `:` 的名称（`ft:…`、Bedrock 的 `-v1:0`、`:free`）；思考预算（`-thinking-1024`，以及 Gemini 名称里不在 `-exp`/`-preview` 后面的 4 位数字，如 `gemini-2.5-flash-1024`）；Qwen 2507 新权重写在 MoE 规格后面的 4 位数字（`qwen/qwen3-235b-a22b-2507`、`qwen/qwen3-30b-a3b-2507` 是 Instruct-2507，和 4 月的 `qwen3-235b-a22b` 不是同一个模型，单独一张卡；它和通义自己写的 `qwen3-235b-a22b-instruct-2507` 也是两张卡）。只有月日、没有年份的日期（`0125`、`1106`）排在带年份的日期之后，按月日从大到小；跨年时顺序可能不对（`gpt-3.5-turbo` 的 `0125` 是 2024 年的，却排在 2023 年的 `1106` 后面）。

**供应商：** 没有在「模型管理」里建记录、或记录里没选供应商的模型，按名称自动识别供应商。规则按固定顺序逐条匹配：`tts-1`、`whisper`、`text-embedding-3-*`、`text-embedding-ada-002`、`omni-moderation`、`dall-e`、`gpt-image`、`sora`、`codex`、`o1`/`o3`/`o4`、`gpt4`/`gpt5` 这类不带 `-` 的写法等都归 OpenAI；Gemma、Imagen、Veo、Lyria、Nano Banana 归 Google；`text-embedding-v*`、通义、万相、QwQ 归阿里巴巴；Seedream、Seedance 归字节跳动，即梦（Dreamina）归即梦。同时去掉了误判：`tongyi-*` 不再算零一万物，`veo3`、`hailuo3` 不再算 OpenAI，`gpt-5.3-codex-spark`、`muse-spark` 不再算讯飞，`inkling` 不再算快手，`gpt-sovits`、`gpt4all`、`tinyllama` 不再算 OpenAI / Meta。名称是 `平台/模型` 时先看斜杠后面的模型名，平台名不决定供应商（`llamagate/nomic-embed-text` 不再算 Meta，`mistral/gpt-oss-120b` 算 OpenAI，`qwencloud/MiniMax-M2.5` 算 MiniMax）；模型名认不出时才看斜杠前的原厂名（`deepseek-ai/janus-pro-7b` 算 DeepSeek）。`@cf/` 开头的 Cloudflare 模型按原厂商归类（`@cf/qwen/...` 归阿里巴巴），认不出原厂商的才归 Cloudflare。微调模型按它名字里的基础模型归类（`hermes-3-llama-...` 算 Meta）。
- 在「模型管理」里建了记录的模型：记录里选了供应商就以记录为准；**记录里没选供应商时也按名称识别**（只看上面的名称规则，不看渠道类型）。比如为了加描述给 `tts-1` 建了记录、没选供应商，它照样显示 OpenAI；按前缀、后缀或包含规则建的记录，匹配到的每个模型各自按自己的名称识别。名称认不出供应商的仍不显示供应商。想显示别的供应商，在记录里选上即可（下拉框里有内置供应商）。升级前，没选供应商的记录会让模型不显示供应商，现在不再这样。
- 用「同步官方元数据」后，记录里的供应商是官方数据里的名字。阿里巴巴、智谱、Moonshot、MiniMax 在官方数据里叫 `Alibaba`、`Z.AI`、`Moonshot AI`、`MiniMax (minimax.io)` 等；保存了这些供应商后，自动识别的同公司模型也归到它们名下，不会再多出一个「阿里巴巴」。官方数据把个别模型记在托管平台名下（例如 `deepseek-v4-pro`、`deepseek-v4-pro-0813` 记为 `Tencent TokenHub`），这类卡片会显示托管平台；需要时在模型管理里改这些记录的供应商。

**左侧筛选栏：** 可以单独显示或隐藏「分组」「供应商」「模型标签」「定价类型」「端点类型」五个筛选项，手机上的「筛选」抽屉也跟着变。默认显示分组、供应商、定价类型，隐藏模型标签和端点类型（老站点升级后也是这样，需要时手动打开）。隐藏的筛选项不显示也不生效：链接里带的 `?tag=`、`?endpointType=` 等参数会被忽略。五个都隐藏时左侧栏整个不显示。

**排序（rc.52 起）：** 排序菜单多了上游的「推荐」「请求最多」「Token 最多」，默认是「推荐」。推荐顺序由 root 在 控制台 → 模型 →「模型广场排序」里设置（手动排序、使用最多、置顶 + 使用最多）；没设置时（默认手动排序、列表为空）卡片顺序和原来默认的按名称排序一样。系列卡片排在它当前显示出来的成员里排名最靠前的那个的位置（例如把 `[按次]x-thinking` 排在第一，整张 `x` 卡片就排在最前），没有成员在列表里的卡片按名称排在后面；卡片里线路和变体的顺序不变。接口只给顺序、不给用量数字，所以不能按一张卡所有变体的用量之和来排。「请求最多」「Token 最多」「使用最多」按最近 1 / 7 / 30 天（默认 7 天）的用量排，用量来自数据看板的统计（`DataExportEnabled`，默认开），没有用量数据时按名称排。用量缓存 5 分钟，同一时间只统计一次（上游每个请求在缓存过期时各查一次库，定制版和排行榜一样合并成一次）；过期后先按旧的用量排（最多再用 5 分钟），后台重新统计。关掉「合并模型变体」时按上游的规则逐个模型排序。

**在哪设置：** 系统设置 → 模型 → 模型广场显示（点「保存」时一次提交，全部生效或全部不生效）：
- 「合并模型变体」开关（默认开），关掉即恢复成每个模型名一张卡（下面两个合并开关随之失效）；
- 「合并版本号写法」（默认开）：上面说的 `-`/`.`、大小写、`厂商/` 前缀；
- 「合并带日期的快照」（默认开）：上面的日期表。关掉后带日期的名称不再并入不带日期的那张卡，紧挨在日期前面的版本号也不再合并写法：`claude-opus-4-5-20251101` 和 `claude-opus-4.5-20251101` 会是两张卡；搜索不受影响，搜 `claude-opus-4.5` 仍能找到 `claude-opus-4-5-20251101`；
- 「左侧筛选栏」：五个筛选项各一个开关；
- 「合并的基础模型」：见下；
- 「变体后缀规则」：从模型名末尾去掉哪些后缀算作变体，以及显示名和说明。默认规则：

| 后缀 | 显示名 | 说明 |
|---|---|---|
| `-thinking` | thinking | |
| `-thinking-minimal / -low / -medium / -high / -xhigh / -max` | thinking-minimal … thinking-max | |
| `-nothinking` | nothinking | 会思考，但不返回思考内容 |
| `-128` | 128 | 思考预算 128（最低思考强度） |

规则从名称末尾按「最长匹配」逐层去除，可以叠加，例如 `-128-nothinking` 显示为「128 · nothinking」；和日期也能叠加，`claude-opus-4-5-20251101-thinking` 显示为「20251101 · thinking」。规则顺序就是变体的显示顺序。变体规则下方有「预览」框，输入一个模型名能看到它的基础模型、合并到哪张卡（「合并为」显示的是合并用的键，不一定是卡片标题：打开「合并版本号写法」时它是小写、版本号写成点、去掉 `厂商/` 前缀的写法，例如 `claude-opus-4-5-thinking` 显示 `claude-opus-4.5`）、线路和变体名；预览不处理 `-latest`（它是否并入要看同页有没有对应的模型）。

**合并的基础模型（按模型名指定）：** 同页「合并的基础模型」里每行填一个基础模型（不带 `[...]` 线路标签），例如 `gemini-4-argon`。之后名称是「基础模型 + `-` + 任意内容」的模型都会并进这张卡，`-` 后面的部分直接作为变体名：
- `[按次]gemini-4-argon-low`、`[按量]gemini-4-argon-medium` → 卡片 `gemini-4-argon`，线路「按次」「按量」，变体 `low`、`medium`；
- 后面还能叠加上表的规则：`gemini-4-argon-medium-nothinking` 显示为「medium · nothinking」；
- 多个基础模型都匹配时取最长的（同时填 `gemini-4` 和 `gemini-4-argon`，`gemini-4-argon-low` 归 `gemini-4-argon`）；`gemini-4-argonx` 这种只是字母相同的不会被并入 `gemini-4-argon`；
- 打开「合并版本号写法」时，基础模型也按同样规则比较：填 `claude-opus-4-5` 会并入 `claude-opus-4.5-thinking-max` 和 `claude-opus-4-5-20251101`（后者这种写法相同的快照，两个合并开关怎么组合都会并入）；基础模型不会截断版本号：填 `claude-opus-4` 只并入 `claude-opus-4` 和它的快照 `claude-opus-4-20250514`，不会吞掉 `claude-opus-4-1`、`claude-opus-4-5-20251101` 这些新版本，填 `claude-sonnet-4` 也不会吞掉 `claude-sonnet-4-5`；关掉「合并版本号写法」时基础模型按原样比较（填 `claude-opus-4` 会并入 `claude-opus-4-1` 等所有 `claude-opus-4-` 开头的模型）；
- 变体顺序：标准在前，接着是只带上表规则后缀的变体（如 nothinking，按规则顺序），然后 minimal / low / medium / high / xhigh / max，其他变体名按字母排，最后是 latest 和日期快照（新的在前）。

适合只想合并个别模型、又不想加全局 `-low / -high` 规则（会误伤 `gpt-5.1-codex-max` 这类模型）的情况，也适合自动规则合并不了的写法（Fireworks 的 `llama-v3p1`、Azure 的 `gpt-35-turbo`、`mistral-medium-3` 与 `mistral-medium-2604` 这类别名）。名称和版本号之间的 `-`（`llama-3.3-70b` 与 `llama3.3-70b`）基础模型也合并不了。

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
- 例外：关闭「审查失败时放行」时，如果审查调用返回 401、403 或 429 `insufficient_quota`（失败原因 `http_401`、`http_403`、`http_429_quota`，说明这个 KEY 或整个 OpenAI 账号不能用了），请求同样**不在这个渠道上发送**，但按「这个渠道的上游返回了这个错误」处理：自动禁用规则照常生效（状态码、关键词，多 KEY 渠道只禁用这一个 KEY），并按正常的重试规则重试（「重试次数」默认 0，即不重试）。重试和上游出错时一样按优先级选渠道，可能还是选到这个渠道和这个 KEY，例如它是唯一的或最低优先级的渠道，或者同一优先级里随机又选中它；多 KEY 渠道可能换到另一个 KEY。重试到的渠道按它自己的审查设置检查，没开审查的渠道会直接转发。每次这样的尝试在服务器日志里打一行 ERROR `channel error …`（见「看记录」）；在 compose 里设置了 `ERROR_LOG_ENABLED=true`（见第十三节，默认不开）时，使用日志里还会记一条错误记录，用户看到的是 503「内容审查暂不可用，请求未发送」，管理员在详情里能看到审查调用的状态码和（隐去 KEY 的）错误信息。只有这一个渠道、又没打开自动禁用时，一个请求会在这个 KEY 上失败「重试次数」+1 次，每次都打一行 ERROR，「不可用的密钥」里也列出这么多次。最后一次尝试就是这样失败的（重试次数用完、按重试规则不再重试，或已经没有可选的渠道，例如唯一的渠道刚被自动禁用）时，用户收到 503，整个请求只记一条「内容审查」记录，详情「内容审查」小节的「不可用的密钥」一行（接口字段 `key_failures`）列出每次这样失败的渠道、第几次尝试（与这个请求「重试链路」中的位置一致）、KEY 序号（从 1 开始，与渠道「多密钥管理」列表一致，只有多 KEY 渠道才有）和原因；重试到的渠道被它自己的审查拒绝时，用户收到那次拒绝（400 或 503），那条「内容审查」记录同样列出之前的「不可用的密钥」；重试到的渠道的上游自己返回错误时，用户收到的是那个渠道的错误，不记「内容审查」记录，之前的 KEY 级失败只留在服务器日志里（设置了 `ERROR_LOG_ENABLED=true` 时还有每次尝试的错误记录）。已经建立的 Responses WebSocket 连接锁定在一个渠道上，不能换渠道，这时只拒绝这一条请求，不禁用 KEY；客户端重连后的第一条请求会走上面的流程。
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
- 和审查开关相互独立，也只对 OpenAI 类型的渠道有效。打开后，发往上游的 Chat Completions 和 Responses 请求里的 `safety_identifier` 一律换成网关按用户生成的标识（`u_` 加 32 位十六进制，同一用户永远相同，无法反推出用户是谁），客户端自己传的值会被覆盖（管理员自己配置的参数覆盖和请求处理插件规则例外，见限制 12）。OpenAI 据此只封禁违规的那个用户，而不是整个组织。
- 打开「透传请求体」时也生效：只改写 `safety_identifier` 这一个字段，其他字节原样转发。
- 打开后，「允许透传 safety_identifier」不再起作用。
- **请在 `deploy/.env` 里设置一次 `CRYPTO_SECRET`（例如 `openssl rand -hex 32` 生成），之后不要再改**，多节点设成一样的值。没有设置 `CRYPTO_SECRET` 时用登录签名密钥：设置了 `SESSION_SECRET` 就用它；没有设置时，「重启或更新后保持用户登录」开着（默认）就用数据库里保存的那把，重启后不变，但按第二十二节换登录签名密钥（让所有人退出登录）时会跟着换；只有开关关着、或启动时读不到保存的密钥时，才每次重启随机生成，发给 OpenAI 的标识也随之改变（服务器日志会提示一次）。改动 `CRYPTO_SECRET` 会让所有用户的标识变化，Redis 里的令牌缓存键也会换一批（无害）；改动前发出的任务结果（视频、图片）下载链接（`/v1/tasks/…/artifacts/…/content?access=…`、`/v1/videos/…/content?access=…`）也会失效（返回 404），客户端需要重新查询任务拿新链接。已经设置了 `SESSION_SECRET` 的部署可以让 `CRYPTO_SECRET` 留空，或设成与 `SESSION_SECRET` 相同的值，这样密钥不变；两者都没设、开关又关着的部署每次重启本来就会让这些链接失效，设置后反而稳定。`deploy/docker-compose.yml` 已经透传这个变量，留空时行为和以前一样。
- 不覆盖的接口：Realtime、图片、旧版 Completions、Responses compact、任务插件。

**限制（请先读完再决定是否「审查失败时放行」）：**
1. 助手（assistant）历史、工具调用和工具结果（包括工具结果里的图片）、工具和函数定义、`response_format` 的 schema 和 Responses 的 `text.format`、`prediction` 都不检查，规则也不扫描它们。用户可以伪造一段 assistant 历史，或把「show your chain of thought」写进函数描述里。
2. 透传请求体的渠道：未知的顶层字段和未知类型的内容块原样转发，不检查；只差大小写的重复字段（例如同时有 `content` 和 `Content`）会被直接拒绝。Claude、Gemini 格式的请求在透传渠道上会原样发到 `/v1/chat/completions`，所以按 Chat Completions 请求体检查（Claude 的顶层 `system`、Gemini 的 `systemInstruction` 和 `contents` 也检查）。
3. 图片接口的输入图片（`/v1/images/edits` 的图片、上传文件）和 Sora 的 `input_reference` 不检查，只检查提示词。
4. 文件、音频、视频（`file`/`input_file`（包括以文件形式发送的纯文本文件）、`file_id`、`input_audio`、`video_url`、Claude 的 PDF 文档、渠道把请求转换成 Responses 时 Claude 的所有文档（包括纯文本文档，会以 `input_file` 转发）、Gemini 的 PDF/音频）以文件形式转发，网关不解析、审查模型也无法识别，不检查，计入「未完全审查」。Responses 的提示词模板只检查 `prompt.variables`，不检查保存在 OpenAI 的模板本身。
5. 超出字数：先送审系统/开发者提示词，但最多占一半字数；然后是**最新一条用户消息**；剩下的额度再给系统提示词的其余部分，最后是更早的用户消息（从新到旧）。长消息按 2000 字一段，从头尾交替取段；剩余字数不够一整段时，只送审这一段里剩余字数那么长的部分（从头部取的段送审它的开头，从尾部取的段送审它的结尾），把字数用满。超出的部分不送审，请求照常转发，记「未完全审查」。用长系统提示词或大量短系统消息已经挤不掉最新一条用户消息了（系统提示词在第一步最多占一半字数，也最多占 256 段候选里的一半），但仍有两个缺口：一条比字数上限还长的用户消息，它中间的部分不送审；更早的历史消息可能不送审。本地规则另有自己的扫描范围（最新用户消息 → 系统提示词 → 更早的用户消息，共 256 KB，每段 128 KB），不受这个字数影响。送审和规则扫描之前，网关先规范化文本，每个请求最多规范化约 4 MB 原文（重试到转发方式不同的渠道时另算一份）：重复的消息和只含空白的消息也计入，规范化后变长的（例如 NFKC 会把 `ﷺ` 展开成 18 个字）按变长后的大小计，但每条消息最多按约 1 MB 计（变长后超过 1 MB 的只保留首尾各约 512 KB）；所以一条接近 1 MB、全是 `ﷺ` 的消息实际要规范化约 11 MB 文字，最坏情况下每种转换方式要多花几秒 CPU。按「最新一条用户消息 → 系统提示词 → 更早的用户消息（从新到旧）」的顺序处理，超出的消息既不送审也不被本地规则扫描，审查模块也不保留它们的原文，记「未完全审查」。最新一条用户消息和第一段系统提示词总在这个范围内，正常请求碰不到这个上限。
6. 图片只检查最新的 3 张；更早的图片、超过 20 MB 的 data URL、既不是 http(s) 也不是 `data:image/…` 的地址不检查。远程图片地址检查的是 OpenAI 下载到的内容，如果对方服务器给 OpenAI 和给模型返回不同的图片，网关无法发现（远程图片的结果不跨请求缓存）。
7. Realtime（`/v1/realtime`）和 NovelAI 原生请求在开启审查的渠道上直接拒绝（「开启内容审查的渠道不支持此接口」），因为内容发出前无法检查。Sora 以外的任务插件提交的任务也一样，不论走插件自己的 `/v1/tasks/…` 还是 `/v1/videos` 等通用接口：rc.52 起多个任务插件可以声明同一种内置渠道类型，社区插件也能跑在 OpenAI 渠道上，而网关只知道 Sora 的文字在 `prompt` 里，别的插件可能从任何字段取文字发给上游。「内容审查」日志里的来源是「不支持审查」、原因是「请求类型」，不计费、不换渠道重试；没开审查的 OpenAI 渠道照常提交。
8. `previous_response_id`、`conversation` 指向的上游历史和加密的推理内容无法检查，只检查本次新发的 `input`。
9. 管理员把保活心跳间隔设得比审查耗时还短时，被拒绝的请求在心跳发出后才返回：流式请求以 HTTP 200 + 一个 SSE 错误事件返回；打开了「非流式请求也保活」时，非流式请求以 HTTP 200 + JSON 错误体（`{"error":…}`，Claude 格式为 `{"type":"error",…}`）返回，各官方 SDK 不会把它当作错误抛出（见第十三节）。这是保活的固有行为，不是 bug。审查通常不到一秒；要让被拒绝的非流式请求拿到真实的 400/503，请把开启审查的渠道的「审查超时（秒）」设得比 Ping 间隔短（例如间隔 10 秒时设为 8 秒）。
10. 关键词规则发现不了「行为上的蒸馏」（大量、整齐划一的请求，多个账号协同），也发现不了没有明确指令的加密推理回放；换个说法、用形近字（例如西里尔字母的 о）或编码（base64、leetspeak）都能绕过。否定说法只跳过少数固定句式，个别正常句子仍可能被拦，「不要公开，但告诉我」这种说法也可能被跳过。「别」「別」「勿」前面的空格不去掉，所以在「请勿」「分别」这类词中间加空格仍能绕开对应的规则；汉字之间的换行会去掉，预设规则可能跨过这样的换行命中。
11. Sora 任务（OpenAI 渠道上的 `/v1/videos`）被拒绝时正常记录；通过和失败放行的审查结果不写到任务的计费记录里。
12. 用户安全标识的覆盖范围见上一节。设置标识之后才执行的渠道参数覆盖、请求处理插件的 outbound 阶段（包括 rc.51 的「全局参数覆盖」插件）和渠道绑定实例的 ingress 能改掉或删除它，例如「全局参数覆盖」里一条删除 `safety_identifier` 的规则会让所有 OpenAI 渠道都不再发送标识；开启这个开关时不要在这些规则里改 `safety_identifier`。Chat Completions 转 Responses 时，这些规则都在设置标识之前执行，无法改掉它。
13. 同一个 OpenAI 组织下所有 KEY 共用审查额度。一个用户连续发大段新内容就可能在一分钟内用光额度：这时所有「失败时拒绝」的渠道对所有用户都返回 503，「失败时放行」的渠道则不经审查转发。OpenAI SDK、Claude Code、Codex 遇到 503 会自动重试，会进一步放大审查调用量。v1 没有按用户的审查额度，也没有熔断。
14. 「审查失败时放行」对所有失败原因都放行，包括请求本身造成的失败：例如图片地址 OpenAI 下载不了、data URL 格式错误。在放行的渠道上，这样的请求（连同图片）会不经审查转发；在拒绝的渠道上返回 503。
15. 参数覆盖在审查之后执行：如果覆盖规则把客户端的请求头或未检查的字段复制进 `messages`、`input`、`instructions`，这部分不会被审查。渠道的系统提示词、参数覆盖里写的内容也不检查。rc.51 起的请求处理插件同理：全局实例和请求所在分组的实例在请求开始时 ingress 阶段的改写会被审查；渠道绑定的实例（渠道的「请求处理插件」）和自动分组跨分组重试后才生效的分组实例，它们的 ingress 在每次尝试的审查之后才执行，和 outbound 阶段的改写一样不会被审查（视频任务 `/v1/videos` 例外：每次尝试先执行这些 ingress，再按改写后的提示词审查）。rc.52 起插件可以声明作用范围（root 确认一次后自动生效）：只限定模型的实例和全局实例一样在请求开始时执行 ingress，会被审查；限定了渠道类型的实例要等选定渠道后才生效，它的 ingress 和渠道绑定的实例一样在审查之后执行，不会被审查（`/v1/videos` 的例外同上）。插件只有 root 能配置；不要把会添加、解码或还原文字的插件绑定到开启审查的渠道上，也不要让这类插件的作用范围包含开启审查的渠道类型。
16. OpenAI 自 2026-11-20 起对「模型接入中间商」的使用政策调整（需要事先获批）不是代码能解决的，请自行评估。

**以后可能加的选项（v1 没有）：** 「无法完整审查时拒绝请求」（超出字数、图片被跳过、有无法检查的内容时一律拒绝）；按用户的审查额度。需要时提出来。

**升级说明：**
- 不需要数据库迁移。设置保存在渠道的 `settings` JSON 里（键名都以 `moderation_` 开头，另有 `send_safety_identifier`），新日志类型的值是 100，存进已有的 `type` 列。
- 不打开开关的渠道行为完全不变。
- 多节点部署：**所有节点都升级之后再打开开关**，还在跑旧版本的节点会不经审查直接转发。开了 Redis 时审查结果在节点间共享。用户安全标识需要所有节点的 `CRYPTO_SECRET` 相同。

**回滚：** 旧版本不认识这些设置，不审查、也不发送用户安全标识；但旧版本在改写渠道设置时（例如上游模型自动更新任务）可能把这些键**丢掉**，再升级回来后请逐个检查开启了审查的渠道。已有的「内容审查」日志会保留，在旧版控制台里显示为类型「未知」，类型筛选选不到。没有其他需要撤销的。

**排查（「测试审查接口」和日志里的失败原因）：**
- `http_401` / `http_403`：KEY 无效或被吊销、账号被封、没有审查权限，或项目的模型白名单里没有 `omni-moderation-latest`。`http_429_quota`：组织额度/余额用完（`insufficient_quota`）。这三种在「失败时拒绝」的渠道上按渠道错误处理（见上面「怎么开」）：打开了自动禁用且状态码在「自动禁用状态码」里（默认只有 401）或错误信息命中禁用关键词时，渠道（多 KEY 渠道是这一个 KEY）会被自动禁用。之后按正常的重试规则处理：按「重试次数」和「自动重试状态码」决定是否重试（默认的自动重试状态码包含 401、403、429），重试选到的渠道（可能还是这个渠道）按它自己的审查设置检查；「重试次数」为 0（默认）时不重试，直接返回 503「内容审查暂不可用，请求未发送」。没打开自动禁用时，这个 KEY 不会被禁用，每次选到它（包括同一个请求的重试又选回它）都会在这里失败一次，然后同样按上面的规则重试或返回 503。默认要找是哪个渠道的哪个 KEY，看两处：请求最终被拒绝时那条「内容审查」记录详情「内容审查」小节的「不可用的密钥」（接口字段 `key_failures`：每次这样失败的渠道、第几次尝试（与「重试链路」中的位置一致）、KEY 序号（从 1 开始）和原因），以及服务器日志里每次尝试的 ERROR `channel error (channel #…, status code: …)` 行（中文日志 `渠道请求出错 (渠道 #…，状态码 …)`；这一行不含 KEY 序号。换到别的渠道后成功时没有「内容审查」记录，但那条消费记录的详情里，「请求策略决策」列出这次失败的渠道和状态码（显示为「上游请求失败 · #… · HTTP …」，看不出是审查调用，也不含 KEY 序号），「渠道」列的「重试链路」列出经过的渠道；那个渠道的上游自己出错时，只有这一行）。在 compose 里设置 `ERROR_LOG_ENABLED=true`（见第十三节）后，使用日志里每次尝试还有一条错误记录（错误码 `content_moderation_unavailable`，详情里有审查失败原因；KEY 序号不在详情里，显示在这条记录「渠道」列的小圆标和悬停提示里，从 0 开始，比「不可用的密钥」里的序号小 1）。「测试审查接口」只测第一个启用的 KEY，多 KEY 渠道里排在后面的坏 KEY 测不出来。
- `http_404`：API 地址没有 `/v1/moderations`（很多转售站、自建网关不支持）。这样的渠道要么打开「审查失败时放行」，要么不要开审查。
- `http_429_rate`：请求或 token 速率超限（见上面的 Tier 额度）；`http_429_size`：单次审查请求就超过了每分钟 token 额度，请调低「每次请求最多审查字数」或提升 Tier。
- `image_rejected`：OpenAI 下载不了用户的图片地址。
- `config`：渠道的请求头覆盖写错了，请修改。
- `timeout`：审查调用超时，可适当调大「审查超时（秒）」。
- `network`：连不上 API 地址或代理配置错误；`http_5xx`：OpenAI 服务端错误；`decode`：返回的不是审查接口的格式（常见于 API 地址指向了别的服务）；`http_other`：其他状态码。
- 被拒绝的请求在服务器日志里有一行 WARN `content moderation rejected a request on channel #…: source=…, detail=…`（来源 `openai`/`rule`/`unavailable`/`unsupported`），对应的 `relay error:` 是 INFO。

## 十九、找回密码与登录防护（自建验证码、重置邮件限额、登录锁定与审计）

**是什么：** 五项自建措施，都不依赖任何第三方服务，也**不依赖 Cloudflare Turnstile**：
1. 「忘记密码」页面和每次用户名 + 密码登录都要填算术图形验证码（和注册页同一种验证码，一张图只能用一次）；
2. 同一邮箱的重置邮件有冷却时间和 24 小时滚动上限；
3. 同一账号（或同一个输入的用户名）密码连续输错后，一段时间内拒绝密码登录；
4. 每次重置请求、通过邮件链接重置的结果、登录失败、两步验证失败、解除锁定，都连同 IP 写进审计日志；
5. 管理员账号开启两步验证、绑定 Passkey（见下面「登录锁定」和「被锁且进不去时」）。

**默认值（升级后立即生效）：** 位置：系统设置 → 身份验证 → 机器人保护（和注册图形验证码、注册 IP 限流在同一张卡片）。改动不用重启，下一次请求就按新设置执行。

| 设置（选项名） | 默认 | 范围 |
|---|---|---|
| 找回密码验证码（`PasswordResetCaptchaEnabled`） | 开 | |
| 重置邮件限额（`PasswordResetEmailLimitEnabled`） | 开 | |
| 冷却时间（秒）（`PasswordResetEmailCooldownSeconds`） | 120 | 30～3600 |
| 每个邮箱 24 小时内上限（`PasswordResetEmailDailyMax`） | 5 | 1～100 |
| 登录验证码（`LoginCaptchaEnabled`） | 开 | |
| 登录失败锁定（`LoginLockoutEnabled`） | 开 | |
| 锁定前允许的失败次数（`LoginLockoutMaxFailures`） | 10 | 3～100 |
| 锁定时长（分钟）（`LoginLockoutMinutes`） | 15 | 1～1440，同时也是统计失败次数的时间窗口 |

**找回密码：**
- 顺序：IP 限流（关键接口限流，和登录、注册共用）→（Turnstile，仅在管理员开启时）→ 算术验证码 → 邮箱格式 → 每邮箱限额 → 查账号并发信。
- 每邮箱限额按邮箱计算，不管这个邮箱有没有注册：冷却时间内最多 1 封，任意 24 小时内不超过上限（滚动 24 小时，不是自然日）。**被拒绝的请求和验证码错误都不计数**，所以真实用户多点几次不会吃掉当天的 5 次。超过时页面提示要等多久，并提醒「邮件可能要几分钟才到，也可能在垃圾邮件里；收到多封请用最新的一封（之前的链接会失效）」。
- 已注册和未注册的邮箱得到相同的接口返回内容（都是「成功」），但已注册邮箱要同步发信，响应会慢一点；注册页本来就会提示「邮箱已被使用」，所以这不是保密手段。冷却提示里的等待秒数会让别人知道这个邮箱最近有人申请过重置（不说明是否注册）。
- 只给和账号邮箱**完全相同**的地址（不区分大小写）发重置链接：带重音或全角字母、只是被 MySQL 排序规则当作相同的写法不再算这个账号的邮箱，也不会收到它的重置链接。
- **已知取舍：** 任何人都能用掉某个邮箱当天的 5 封。邮箱主人只能等待，或请管理员在用户管理里点「解除登录锁定」（同时清除这个邮箱的重置邮件计数），或由管理员直接改密码。
- 通过邮件链接重置成功后，这个账号的登录锁定也会解除。
- 重置链接发出后 10 分钟内有效（没有改变）；每次新申请都会让同一邮箱之前的链接失效。rc.52 起（上游的安全修复）链接只能用一次，用它重置时就作废，之后即使服务器出错也要重新申请；邮件没发出去时，这封邮件里的链接随即作废；链接里的邮箱和收件地址都用账号里保存的地址。

**登录验证码：**
- 每次用户名 + 密码登录都要填（包括开启了密码加密传输的情况）；两步验证那一步、Passkey、第三方登录不需要。验证码错误不算密码错误，不计入锁定，但会写一条审计。
- **影响：** 通过接口用用户名和密码登录的脚本和应用（例如签到脚本）在开启期间无法登录；`sk-` 开头的 API 密钥不受影响。需要时可以暂时关闭「登录验证码」。
- **验证码图片限流**（按客户端网络计算：IPv4 按地址，IPv6 按 /64）：只要开启了任一验证码，并且没有关闭关键接口限流（`CRITICAL_RATE_LIMIT_ENABLE`，默认开，20 次 / 20 分钟），每个网络默认每 20 分钟最多 60 张（关键接口限流每允许一次请求给 3 张）；打开「注册 IP 限流」时按注册窗口计算（默认每 15 分钟 30 + 60 = 90 张）；关闭了关键接口限流又没开注册 IP 限流时不限（服务器在不受信任的反向代理后面时，所有访客的 IP 都一样，先用这张卡片里「服务器看到的你的 IP 地址」确认拿到的是真实 IP）。没有 Redis 时服务器最多同时保存 10 万个未作答的验证码。

**登录锁定：**
- 规则：用户名和邮箱登录同一账号时共用一个计数；不存在的用户名同样计数和锁定（大小写、重音、全角写法算同一个名字）。只用一个名字尝试时，锁定提示对存在和不存在的账号完全相同；但同一账号的用户名和邮箱共用一个计数，所以交替用一个用户名和一个邮箱各输错几次（默认合计 10 次，要过 10 次验证码，并会把这个账号锁住 15 分钟），能看出这两个名字是否属于同一个已存在的账号；一个账号被锁后，它的用户名和邮箱也都会提示锁定，已经猜到两者的人同样能借此看出它们属于同一个（存在的）账号。在「锁定时长」内密码输错达到「锁定前允许的失败次数」时，这一次就直接提示锁定；锁定期间即使密码正确也拒绝；锁定期间的尝试不计数，也不延长锁定。同时发来的大量请求，最多也只有「失败次数」那么多次会真正检查密码，其余直接按锁定拒绝。
- 任何方式登录成功（密码登录有两步验证时是两步验证也完成；Passkey、第三方登录也算）后计数清零、锁定解除；只输对密码、两步验证还没完成时不清零。密码登录流程里两步验证码输错也计入同一个账号的计数，锁定时两步验证那一步也被拒绝；原有的「两步验证 5 次错误锁 5 分钟」仍然有效。账号被禁用时，输对密码也只提示通用的「用户名或密码错误，或用户已被封禁」，并且和输错密码一样计入失败次数（否则锁定出现的时机会透露密码是否正确），审计里记为「账号已禁用」。
- 解锁途径：等锁定时长过去；通过邮件链接重置密码；管理员在用户管理的操作菜单里点「解除登录锁定」（需要二次验证并写审计，root 可以给自己解除，管理员只能给普通用户解除）；Passkey 和第三方登录不受锁定影响（登录成功后锁定随即解除）——前提是站点开启了 Passkey 登录、账号绑定了 Passkey（或绑定了已开启的第三方登录）。
- **两步验证不能解除锁定**，它的作用是密码被猜中也登不进去。
- **已知取舍：** 知道用户名的人可以故意把某个账号（例如 `root`）一直锁住（默认每 15 分钟 10 次错误密码就够，IP 限流挡不住）。建议：给 root 绑定 Passkey 并开启 Passkey 登录（被锁时的正门）；root 和管理员开启两步验证；root 不要用默认用户名 `root`；必要时调大失败次数或缩短锁定时长。真被锁且进不去时按下一条操作。

**被锁且进不去时的应急办法**（在 `deploy` 目录执行）：
- 还有一个已登录的 root 会话（锁定不会让已登录的会话下线；「重启或更新后保持用户登录」开着（默认）或设置了 `SESSION_SECRET` 时，重启和升级也不会让它下线，见第二十二节）：用户管理 → 找到自己 → 「解除登录锁定」（二次验证可以用两步验证、Passkey 或密码，锁定不影响二次验证）。
- 用了 Redis（本仓库的 compose 默认有）：清除所有登录锁定和失败计数（不影响重置邮件计数）：
  `docker compose exec -T redis redis-cli --scan --pattern 'authguard:v1:login:*' | xargs -r docker compose exec -T redis redis-cli del`
  （Redis 设了密码时两处 `redis-cli` 后面都加 `-a <密码>`；服务名以 compose 文件为准。）
- 没有 Redis：`docker compose restart new-api`（计数在内存里，重启即清空）。
- 临时关闭登录锁定（直接改数据库里的设置，最多等 60 秒 `SYNC_FREQUENCY` 生效，或重启）：
  MySQL：``INSERT INTO options (`key`, value) VALUES ('LoginLockoutEnabled', 'false') ON DUPLICATE KEY UPDATE value = 'false';``
  PostgreSQL / SQLite：`INSERT INTO options ("key", value) VALUES ('LoginLockoutEnabled', 'false') ON CONFLICT ("key") DO UPDATE SET value = 'false';`
  （升级后没改过设置时表里没有这一行，所以用 INSERT。）登录成功会解除这个账号的锁定，之后再到设置里重新打开「登录失败锁定」；其他账号还没到期的锁定在重新打开后会继续生效，需要时在用户管理里给它们「解除登录锁定」。

**关于 Cloudflare Turnstile：** 可选。Cloudflare Turnstile 在中国大陆很多用户加载不出来，不建议作为主要防护，也不建议为了本节功能去开启它。本节所有措施都不依赖它；开启后只是在 IP 限流之后多一道检查。

**多节点：** 配置了 Redis（`REDIS_CONN_STRING`）时，计数和锁定在所有节点之间共享（键前缀 `authguard:v1:`，自动过期；键名里是邮箱 / 用户名的 SHA-256，不是明文，但能读 Redis 的人可以拿字典去对）。没有 Redis 时每个节点各自计数。Redis 出错时自动改用本节点的内存计数（日志里有 `auth guard: Redis failed, using the in-memory store`，`DEFAULT_LANGUAGE=zh-CN` 时是「登录与找回密码防护：Redis 出错，改用内存计数」），恢复后继续用 Redis。所有节点都升级之后才完全生效。重置链接（令牌）本来就只保存在发出邮件的那个节点的内存里（没有改变）：多节点且没有会话保持时，在另一个节点打开链接会提示无效。本仓库的部署只有一个应用节点，不受影响。

**审计日志：**
- 控制台 → 审计日志，类别「登录」。事件：申请重置密码 + 结果（已发送邮件、该邮箱未注册、邮件发送失败（附原因：邮件服务器出错 / 该邮箱对应多个账号 / 查询账号失败）、申请过于频繁、已达每日上限、验证码缺失或错误、邮箱格式无效）、通过邮件链接重置密码成功 / 失败、登录失败 + 原因（用户名或密码错误、账号已禁用、失败次数过多已锁定、验证码缺失或错误、请求无效；达到失败次数的那一次标「已锁定密码登录」）、登录时两步验证失败、解除登录锁定（类别「操作」，记在操作的管理员名下）。
- 可以按事件、IP、邮箱（只查找回密码相关的记录）、用户名、结果、时间筛选。
- 登录失败时输入的内容如果不是任何账号的用户名或邮箱、也不是邮箱格式（常见情况是把密码输进了用户名框；找回密码时填的不是邮箱格式的内容也一样），这段内容本身不保存，root 也看不到，数据库和备份里也没有：记录里只有「…」、长度和「输入内容指纹」。指纹是以 `CRYPTO_SECRET`（没设时用 `SESSION_SECRET`）为密钥，对去掉首尾空格的内容（找回密码时先转成小写）算出的 HMAC-SHA256 的前 8 位十六进制：同一段内容的指纹总是相同，能看出是不是在反复试同一个值，但不能从指纹还原出内容（拿到了密钥的人可以拿猜测的值逐个去对）。两个变量都没设时，每次启动随机生成一把指纹密钥，重启前后的指纹对不上（设置方法见第十八节 `CRYPTO_SECRET` 的说明）；「重启或更新后保持用户登录」开着时也一样：数据库里保存的那把登录签名密钥不用来算指纹（第二十二节），所以数据库副本和备份里没有能核对指纹的密钥。登录时查账号出错写的服务器日志里也只有这个指纹。找回密码是 GET 请求，填的内容和以前一样会出现在访问日志的请求地址里。审计里不记录密码框里的内容、验证码和重置令牌。
- 谁能看到：root 看全部；管理员和有「审计日志」权限的副管理员看普通用户、管理员和匿名请求的记录；与 root 账号有关的记录（包括针对 root 的登录失败）只有 root 能看；用户在自己的审计记录里能看到针对自己账号的记录。
- 不写审计的请求：被 IP 限流或 Turnstile 拦下的（只在访问日志里）；管理员关闭了密码登录时的登录请求；查账号时数据库出错的登录请求；两步验证那一步的登录流程已失效或两步验证已被锁定时。

**IPv6 与审计记录增长：** 关键接口限流按完整 IP 计算，IPv6 客户端可以换地址绕过，所以「没填验证码」的请求写入的审计记录不受单个 IP 的限制（账号锁定和验证码本身不受影响）。审计表不会自动清理；需要时在反向代理上按 /64 限流，或定期删除旧的匿名失败记录（可选，MySQL 示例；单独配置了日志库 `LOG_SQL_DSN` 时在日志库执行）：
`DELETE FROM audit_logs WHERE user_id = 0 AND action IN ('user.login_failed', 'user.password_reset_request') AND created_at < UNIX_TIMESTAMP() - 90 * 86400;`

**从访问日志里查：** `docker compose logs --since 72h 2>&1 | grep reset_password | awk -F'|' '{print $7}'`，再接 `| sort | uniq -c | sort -rn` 看谁在刷。访问日志的格式是 `[GIN] 时间 | 标签 | 请求ID | 状态码 | 耗时 | IP | 方法 路径`，`docker compose logs` 会在每行前面加上 `容器名 |`，所以 IP 在第 7 列；直接看容器里 `/app/logs` 的日志文件时 IP 在第 6 列。IP 两边有对齐用的空格，不影响 `sort | uniq -c`。

**升级说明：**
- 没有数据库结构变化；升级后四个开关默认开启（登录验证码、找回密码验证码、重置邮件限额、登录失败锁定）。
- **升级前：** ① 给 root 绑定 Passkey 并在设置里开启 Passkey 登录，或确认 root 有能收信的邮箱且 SMTP 正常；② 升级前确认 ① 已经生效（root 能用 Passkey 登录，或能收到重置邮件）。从还没有第二十二节功能的版本升级时，所有登录会话照样会失效一次，所以升级前保留的 root 会话不能当作退路（升级到那个版本以后，「重启或更新后保持用户登录」开着时，之后的重启和升级不再让会话失效）；③ 考虑把 root 的用户名改掉；④ 通知用脚本登录的用户（`sk-` API 密钥和访问令牌不受影响）。
- **升级后：** 已经打开的浏览器页面还是旧的前端脚本，登录 / 找回密码会提示「请先完成图形验证码。」（英文界面 `Please complete the captcha.`）却没有验证码输入框，刷新页面即可。

**回滚：** 旧版本不认识这些设置，忽略它们（没有验证码、限额和锁定，失败登录不写审计，验证码图片又不限流）。已经写入的审计记录保留，旧版本控制台显示原始事件名。Redis 里的 `authguard:v1:*` 键会自动过期，不需要清理。

**排查：**
- 用户说收不到重置邮件：看审计。「已发送邮件」说明已经发出 → 让用户查垃圾邮件，并用最新的一封；「已发送邮件」之后紧跟一条「链接无效或已过期」：重置链接只在发出后 10 分钟内有效（没有改变），邮件晚到超过 10 分钟或用了旧邮件里的链接都会这样（服务重启也会让未用的链接失效），让用户重新申请（注意冷却时间），收到后尽快打开最新的一封；「邮件发送失败」→ 看原因，检查 SMTP；「申请过于频繁」/「已达每日上限」→ 等待，或管理员「解除登录锁定」。
- 用户说一直提示登录失败次数过多：在审计里按用户名或事件「登录失败」看「已锁定」/「已锁定密码登录」的来源 IP，确认不是本人后由管理员「解除登录锁定」；锁定反复出现说明有人在针对这个账号，建议绑定 Passkey、开启两步验证。
- 服务器日志里出现 `auth guard: Redis failed, using the in-memory store`（中文日志「登录与找回密码防护：Redis 出错，改用内存计数」）：检查 Redis 连接；在恢复之前多节点各自计数。

---

## 二十、访问速度与稳定性：部署和配置建议

适用于「服务器在海外、用户主要在国内」的站点。本节只讲**配置和部署**，大部分不需要升级就能做；标了「本版本起」的条目依赖这次更新里的改动（第 9 条列出了这次更新改了什么、新增了哪些环境变量），升级之后再改。每一条都可以单独做、随时改回去。建议先做第 1 条的测量，再按结果挑着做。

### 1. 先量几个数（约半小时）

| 量什么 | 怎么量 | 怎么看 |
|---|---|---|
| 应用到数据库的往返时间 | 在运行容器的服务器上执行（只建立 TCP 连接，不需要数据库密码）：<br>`for i in 1 2 3 4 5; do bash -c 'time (exec 3<>/dev/tcp/<数据库地址>/3306)' 2>&1 \| grep real; done` | 同机房一般在 1 ms 左右。每个中转请求在数据库上原来有 5 次往返，本版本起写使用日志不再包一层事务，减为 2 次；每次发布或重启要先做约 1,270 次往返才开始响应（见第 5 条）。超过 5 ms 就值得把数据库和应用放到同一机房 |
| 数据库连接数 | 在 MySQL 客户端执行：<br>`SHOW VARIABLES LIKE 'max_connections';`<br>`SHOW GLOBAL STATUS WHERE Variable_name IN ('Max_used_connections','Connection_errors_max_connections','Aborted_connects');` | `Connection_errors_max_connections` 大于 0，说明出现过「数据库连接已满」导致的页面报错，按第 4 条调整 |
| 用户的实际访问链路 | 在国内的一台机器上执行：<br>`curl -so /dev/null -w 'DNS %{time_namelookup}s 连接 %{time_connect}s TLS %{time_appconnect}s 首字节 %{time_starttransfer}s 总计 %{time_total}s HTTP/%{http_version}\n' https://<你的域名>/`<br>或请一位国内用户用 Chrome 打开网站：F12 → Network → 勾选 Disable cache → 刷新 → 在请求列表上右键「Save all as HAR」发给你 | 显示 `HTTP/1.1` 时按第 2 条开启 HTTP/2；DNS 超过 0.3 秒可以换一家国内有节点的解析服务商；首字节远大于连接时间的 2 倍，说明源站或中间代理慢 |
| 后端看到的用户 IP | `docker compose logs --no-log-prefix --tail 3000 new-api \| grep '^\[GIN\]' \| awk -F'\|' '{gsub(/ /,"",$6); print $6}' \| sort \| uniq -c \| sort -rn \| head` | 大部分请求都来自同一两个 IP（CDN 或反代的地址）时，所有用户共用一个限流桶，登录、刷新页面会互相挤占，要配 `TRUSTED_PROXIES`（见第六节和第 2 条） |
| 磁盘和日志 | `df -h / /var/lib/docker; du -sh <部署目录>/logs` | 使用率接近 95% 时，所有 `/v1` 请求都会被过载保护拒绝（503），见第 7 条 |

有条件再看两项：用户浏览器版本（nginx 访问日志 `awk -F'"' '{print $6}' /var/log/nginx/access.log | sort | uniq -c | sort -rn | head -30`，见第 8 条）；后台「系统设置 → 请求策略 → 会话与重试」里的「重试次数」，以及渠道是否开启了内容审查（见第 6 条）。

### 2. 跨境网络（不改代码，收益最大的一类）

**开启 BBR。** 国内到海外的线路丢包多，Linux 默认的拥塞控制一遇到丢包就大幅降速，BBR 受丢包影响小得多。在**向用户发送数据的那台服务器**上执行（海外源站；如果前面还有国内或香港的反代，两台都可以开）：

```bash
uname -r                       # 内核需 4.9 或更新
cat > /etc/sysctl.d/90-newapi-net.conf <<'CONF'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_slow_start_after_idle = 0
CONF
sysctl --system
sysctl net.ipv4.tcp_congestion_control    # 输出 bbr 即已生效
```

撤销：删除这个文件，再执行 `sysctl -w net.ipv4.tcp_congestion_control=cubic net.ipv4.tcp_slow_start_after_idle=1`。nginx 跑在容器里时，改完重启那个容器。

**开启 HTTP/2。** HTTP/1.1 下浏览器对一个站点最多同时开 6 个连接，控制台各页首次打开时同一批要下载 6–10 个 JS/CSS 文件，超过 6 个的部分要多等一个往返。nginx 1.25.1 及以上在 `server` 里写 `http2 on;`，更老的版本写 `listen 443 ssl http2;`。宝塔开启 SSL 后一般已经带 HTTP/2，用第 1 条的 curl 命令确认显示的是 `HTTP/2`。HTTP/3（QUIC）在国内运营商上常被限速，不必追求。

**两层 nginx（国内或香港反代 → 海外 nginx → new-api，见第十三节）时，外层要复用到源站的连接。** nginx 默认每个请求都对源站新建连接，跨境一次 TCP + TLS 握手要 2–3 个往返，比请求本身还慢。外层这样写：

```nginx
upstream newapi_origin {
    server <海外源站域名或IP>:443;
    keepalive 64;
}

server {
    # ……原有的 listen、ssl 配置不变……
    location / {
        proxy_pass https://newapi_origin;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host <海外源站域名>;
        proxy_ssl_server_name on;
        proxy_ssl_name <海外源站域名>;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

同时在**海外**那台的 compose 里，把外层反代的出口 IP 加进 `TRUSTED_PROXIES`，否则所有用户都会被算成这一个 IP（第六节）。

**让静态文件离用户近一些。** `/static/` 下的文件名带内容哈希，源站已经设了一年缓存（第六节），可以放心让 CDN 或外层反代缓存。本版本起源站对接受 brotli 的浏览器直接返回构建时生成的 `.br`，每种压缩格式各有自己的 ETag，所有响应都带 `Vary: Accept-Encoding`，缓存会按压缩格式分别保存，不会把 brotli 发给不支持它的客户端。外层反代缓存的写法：先在 `http {}` 里加一行（宝塔在「软件商店 → Nginx → 设置 → 配置修改」）：

```nginx
proxy_cache_path /var/cache/nginx/newapi levels=1:2 keys_zone=newapi_static:20m max_size=2g inactive=30d use_temp_path=off;
```

再在站点的 `server {}` 里加：

```nginx
location /static/ {
    proxy_pass https://newapi_origin;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host <海外源站域名>;
    proxy_ssl_server_name on;
    proxy_ssl_name <海外源站域名>;
    proxy_cache newapi_static;
    proxy_cache_lock on;
    add_header X-Cache-Status $upstream_cache_status;
}
```

第二次访问同一个文件时，响应头里 `X-Cache-Status: HIT` 就说明命中了。只缓存 `/static/`，页面和接口不要缓存。

### 3. CDN 和服务器位置

- **国内 CDN 节点需要 ICP 备案。** 没有备案只能用香港、日本、新加坡等境外节点，或选对国内线路做了优化的服务商。
- **只让 CDN 缓存 `/static/*`。** `/` 和其他页面路径、`/api/*`、`/v1/*` 一律不缓存、不改写。不要用「缓存所有内容」「忽略源站缓存头」这类规则，否则用户会看到旧页面、旧公告，甚至别的分组的价格。
- **中转接口尽量不经过会掐断长请求的 CDN。** Cloudflare 免费、Pro、Business 套餐在源站约 100 秒没有响应时直接返回 524，非流式的长回答、画图会因此失败；流式请求要开启第十三节的「保持连接心跳」（间隔填 10 秒）。最稳妥的是给 API 单独一个不走 CDN（Cloudflare 里是「仅 DNS」灰云）的子域名，网页走 CDN。CDN 和 nginx 的压缩类型里不要包含 `text/event-stream`。
- **服务器位置。** 大部分用户在国内时，把服务器连同数据库搬到香港、日本或新加坡等对国内有优质线路（如 CN2 GIA、CMI）的机房，网页、接口、中转三类请求的往返会一起缩短，这是 CDN 做不到的。
- **预期收益。** 在实测的 2 Mbit/s、180 ms 往返条件下，只把静态文件放到近处，首次进入从约 6.9 秒降到约 1.1–1.4 秒（接口仍然跨境）。

### 4. docker-compose 建议改动

```yaml
services:
  new-api:
    # ……原有配置不变，新增下面几项……
    stop_grace_period: 35s            # 必须比 SHUTDOWN_TIMEOUT_SECONDS 大
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "3"
    environment:
      # ……原有变量不变，新增或修改：
      - SHUTDOWN_TIMEOUT_SECONDS=25   # 发布时最多等正在输出的回复 25 秒，见第 5 条
      - STREAMING_TIMEOUT=1800        # 单位是秒；原来的 1800000 约等于 20 天
      - REDIS_POOL_SIZE=50

  redis:
    image: redis:<主版本.次版本>      # 不要用 latest；当前版本用 docker exec redis-alpha-us redis-server --version 查
    logging:
      driver: json-file
      options:
        max-size: "20m"
        max-file: "3"
```

逐项说明：

- **日志上限。** 现在 Docker 的标准输出日志没有上限，`logs/` 里的 `oneapi-*.log` 每 100 万行换一个新文件但从不删除。磁盘到 95% 时过载保护会让所有 `/v1` 请求返回 503。`logs/` 里的旧文件可以在后台「系统设置 → 运维 → 性能」里清理，或加一条定时任务：`find <部署目录>/logs -name 'oneapi-*.log' -mtime +14 -delete`。
- **Redis 固定版本。** `latest` 会在 `docker compose pull` 时直接升到新的大版本并重启 Redis。旧版本在 Redis 重启的那几秒里所有 `/api` 请求都返回 500，打开着的控制台会跳到「糟糕！出错了」；本版本起 API 限流在 Redis 出错时临时改用本机内存计数继续限流，不再返回 500（第六节）。即使这样也建议固定版本，自己选时间升级 Redis。
- **`STREAMING_TIMEOUT`。** 它是上游两次输出之间最长允许沉默的秒数。1800000 秒等于不限制：上游卡住时请求会一直挂着，开了心跳后客户端也不会自己超时。1800（30 分钟）足够长推理用，嫌长可以改成 600。
- **`SQL_MAX_OPEN_CONNS`（本版本起）。** 第 1 条查到 `Connection_errors_max_connections` 大于 0，或 `Max_used_connections` 接近 `max_connections` 时，改成 `(max_connections − 20) ÷ new-api 实例数`，例如 `max_connections=151`、一个实例 → `SQL_MAX_OPEN_CONNS=130`。本版本起渠道限额改为后台刷新，连接被占满时选渠道不再排队等这个查询，可以放心调低；旧版本不要调，否则所有渠道选择都会排队等同一个查询。查不出问题就保持不变。`SQL_MAX_LIFETIME` 保持默认的 60 秒，不要调大：外部数据库前面的 NAT 或负载均衡常会断开空闲连接，调大后会偶发 `invalid connection`。
- **Redis 超时（本版本起）。** Redis 卡住（连接还在，但不回应）时，本版本的页面和接口照样能返回，但每次调用 Redis 都要先等到超时，才改用内存计数；一个带登录态的请求要调用好几次 Redis（限流、读用户缓存等），等待会叠加。实测 Redis 完全不回应、连接串里没写超时（用默认值）时：带登录态的 `/api` 请求（控制台、使用日志等）全部返回 200，但中位数要等 11.4 秒、最长 21.3 秒；不带登录态的 `/api/status`、公告、模型广场数据中位数 4.0 秒；`/v1` 中转请求新旧版本一样，中位数约 11.5 秒。旧版本在同样情况下大多 4 秒后返回 500。能做的：
  - 升级到本版本后，把连接串改为 `REDIS_CONN_STRING=redis://redis:6379/0?dial_timeout=1s&read_timeout=1s&write_timeout=1s&pool_timeout=2s&max_retries=1`。每次调用 Redis 最多多等一两秒（按这几个超时估算，没有实测），一个请求总共多等几秒，而不是十几秒。
  - Redis 卡住时尽快重启 Redis（`docker compose restart redis`）。Redis 恢复后 new-api 会自动重新连上，不用重启 new-api。
  - 设了 `RATE_LIMIT_REDIS_FAILURE=reject`（恢复旧行为）或还在用旧版本时不要单独改超时：Redis 一出错就返回 500，超时调短只会让 500 来得更快。连接池大小以 `REDIS_POOL_SIZE` 为准（连接串里的 `pool_size` 不生效）。
- **`TRUSTED_PROXIES` 和页面限流。** 见第六节「发布后建议做的两件事」第 1 条；前面有 CDN 或两层反代时一定要填。前面已经有限流（CDN、WAF）时可以设 `GLOBAL_WEB_RATE_LIMIT_ENABLE=false`，否则按 compose 里的注释设 `GLOBAL_WEB_RATE_LIMIT=3000`。本版本起打开页面被页面限流拦下时，浏览器看到的是「访问过于频繁，N 秒后自动重试」的提示页，到时间自动刷新，不再是空白错误页。
- **登录与刷新登录状态的限流（本版本起）。** 旧版本里刷新登录状态和登录、两步验证、退出共用关键接口限流（每 IP 20 次 / 20 分钟），同一个 IP 20 分钟内打开页面超过约 20 次就会被登出，而且暂时无法登录（一个公司、学校或运营商出口 IP 下的用户共用这 20 次）。本版本起刷新登录状态不再计入 `CRITICAL_RATE_LIMIT`，有单独的额度：每 IP `AUTH_REFRESH_RATE_LIMIT` 次 / `AUTH_REFRESH_RATE_LIMIT_DURATION=1200` 秒，`AUTH_REFRESH_RATE_LIMIT_ENABLE` 默认跟随 `CRITICAL_RATE_LIMIT_ENABLE`（关掉关键接口限流的站点，刷新照旧不限）；刷新暂时失败时，浏览器里有登录记录（服务器写的 `new_api_has_session` Cookie）就显示「暂时无法恢复登录状态」并可重试，不再直接退出登录；没有登录记录的访客照旧去登录页。不设 `AUTH_REFRESH_RATE_LIMIT` 时默认 300 次；`CRITICAL_RATE_LIMIT` 原来调得更大的站点（折算到刷新的窗口超过 300 次，例如 `CRITICAL_RATE_LIMIT=1000`，或 `CRITICAL_RATE_LIMIT_DURATION=60` 时 20 分钟可达 400 次），刷新沿用折算后的次数，不会比升级前少，启动日志里有一行说明；设了 `AUTH_REFRESH_RATE_LIMIT` 就按设的值。以前为此临时加的 `CRITICAL_RATE_LIMIT=60` 升级后可以删掉（它让同一 IP 的密码尝试也放宽到 60 次 / 20 分钟；按账号的登录锁定仍然有效，见第十九节）；删掉或调小 `CRITICAL_RATE_LIMIT` 后，刷新的额度随之回到 300 次（除非设了 `AUTH_REFRESH_RATE_LIMIT`）。很多用户共用一个出口 IP 时如果仍然出现刷新被限流，先确认 `TRUSTED_PROXIES`，再酌情调大 `AUTH_REFRESH_RATE_LIMIT`。

### 5. 发布、重启时少报错

实测（定制版 `df1bb3a`，数据库已初始化）：new-api 启动时要先做约 1,270 次数据库往返（逐个检查表结构），这段时间端口不响应，前面的 nginx 返回 502。

| 应用到数据库的往返 | 启动到能响应 |
|---|---|
| 同一台机器（约 0 ms） | 0.4 秒 |
| 10 ms | 13 秒 |
| 50 ms | 60 秒 |

停止旧容器时，new-api 先关掉端口，再等正在进行的请求（主要是流式回复）结束，最多等 `SHUTDOWN_TIMEOUT_SECONDS`（默认 120 秒）；而 Docker 默认只给 10 秒，到时直接强杀。所以每次发布的不可用时间大约是「等待时间 + 启动时间」。

- 先 `docker compose pull`，再 `docker compose up -d`，不要让 `up` 现场下载镜像。
- 选用户少的时段发布。发布后执行 `docker compose logs --no-log-prefix new-api | grep 'ready in'`，看到的时间就是这次的启动停机时间。
- `SHUTDOWN_TIMEOUT_SECONDS` 是一个取舍：调大，发布时少打断正在生成的长回复，但这段时间里新请求全部失败；调小，发布快，但超过这个时间还没结束的回复会被打断（「生成一半报错」）。上面的 25 秒适合以普通聊天为主的站；以长推理、长代码输出为主的站可以设 60–110，同时把 `stop_grace_period` 设为 70s–120s。**`stop_grace_period` 一定要比它大**，否则 Docker 会在 new-api 保存数据之前强杀：本版本起退出前会先把批量更新里还没写进数据库的额度变化（`BATCH_UPDATE_INTERVAL`，compose 里是 30 秒）写进去，日志里有一行「退出前写入批量更新中尚未落库的额度变化」（英文日志 `flushing batched quota updates before exit`）；被强杀就写不进去，这段时间的用量会丢失。
- 数据库往返超过 5 ms 时，最有效的办法是把数据库和应用放到同一机房。想做到发布完全不停机，需要跑两个 new-api 实例、由 nginx 在两者之间切换，这要单独规划。

### 6. 中转（API 调用）少报错

- **重试和备用渠道。** 后台「系统设置 → 请求策略 → 会话与重试」里的「重试次数」默认是 0，上游偶发的 5xx、429 会直接报给用户。同一模型有备用渠道时设为 1–2（实测换渠道只多花约 1 毫秒）；「渠道健康」里可以开启「重复失败时自动禁用渠道」。
- **上游卡住不回应。** `RELAY_RESPONSE_HEADER_TIMEOUT` 默认 1800 秒：上游接受了连接却一直不回响应头时，请求会一直挂着，也不会换渠道。开了重试、有备用渠道时可以设为 300。注意非流式的长推理、画图可能要几分钟才返回第一个字节，设太小会误杀。
- **长请求保活。** 按第十三节开启「保持连接心跳」，间隔填 10 秒。
- **内容审查。** 渠道开启外部内容审查（第十八节）时，每个请求要先等审查接口返回才发往上游，首字会晚一个审查往返。
- **数据库位置。** 实测（单个非流式中转请求的中位数）：应用和数据库在同一台机器时新旧版本都约 3 ms；数据库往返增加 2 ms 时，旧版本 15.6 ms，本版本 7.4 ms；增加 10 ms 时，旧版本 57.8 ms，本版本 24.4 ms，8 个并发时每秒能处理的请求从 131 个增加到 301 个。本版本起写使用日志少了 3 次数据库往返，数据库越远省得越多；但每个请求仍要等好几个往返，能放同一机房就放同一机房（第 5 条）。

### 7. 过载保护（突然「全部 503」时先看这里）

后台「系统设置 → 运维 → 性能」里的性能监控默认开启：CPU 90%、内存 90%、磁盘 95%，连续超过阈值时**所有 `/v1` 请求返回 503**（网页和控制台不受影响）。磁盘按容器临时目录所在的分区算，一般就是 Docker 所在的磁盘，日志写满磁盘是最常见的原因（第 4 条）。容器没有设 CPU、内存上限时按整台机器算，同机的其他程序也会触发。

### 8. 老浏览器

当前前端支持 Chrome / Edge 107 及以上、Firefox 104 及以上、Safari 16 及以上（iOS 16）。旧版本里更老的浏览器（例如 iOS 15 上的微信内置浏览器、很旧的安卓 WebView）会白屏；本版本起会显示「浏览器版本过低，请升级或换用系统浏览器打开」的提示。如果第 1 条的访问日志里这类用户较多，再考虑放宽兼容范围（会增加一些下载体积）。

### 9. 这次更新改了什么、新增了哪些设置

**新增的环境变量**（都有默认值，不设就是下面的默认行为）：

| 变量 | 默认 | 作用 |
|---|---|---|
| `AUTH_REFRESH_RATE_LIMIT_ENABLE` | 跟随 `CRITICAL_RATE_LIMIT_ENABLE`（默认开） | 刷新登录状态（`POST /api/user/auth/refresh`）是否限流 |
| `AUTH_REFRESH_RATE_LIMIT` | `300`；`CRITICAL_RATE_LIMIT` 折算到刷新窗口后更多时取它 | 每个 IP 在一个窗口内最多刷新多少次。刷新不再计入 `CRITICAL_RATE_LIMIT`，原来把它调到超过 300 次 / 20 分钟的站点不设这个变量也不会变少（见第 4 条）；不是正数时用默认值 |
| `AUTH_REFRESH_RATE_LIMIT_DURATION` | `1200`（秒） | 刷新限流的窗口；不要超过 1200（内存计数 20 分钟不活动就清掉） |
| `RATE_LIMIT_REDIS_FAILURE` | `memory` | Redis 出错时 API 层和按用户的限流怎么办：`memory` 临时改用本机内存计数继续限流；`reject` 恢复旧行为，直接返回 500。页面和静态文件的限流不受影响，Redis 出错时照旧放行 |

**行为变化**（除前两项是有意改变的默认行为外，其余对用户可见的功能不变）：

- 刷新登录状态单独限流（见第 4 条）；刷新暂时失败时不再直接退出登录，限流提示按界面语言显示并说明等待秒数。
- Redis 出错时 API 限流改用内存计数，不再整站返回 500（第六节）；多节点时每个节点各自计数，等于暂时放宽到 N 倍；日志每个限流器最多 10 秒一条「限流检查失败，改用内存限流」。
- 停机前写入批量更新中尚未落库的额度变化（第 5 条）。
- 写使用日志不再包一层事务（每个中转请求少 3 次数据库往返；日志库是 ClickHouse 时照旧走事务，它的写入要在事务提交时才真正发出）；渠道限额改为后台刷新，选渠道不再等数据库查询。
- 排行榜同一时间只构建一次，缓存过期后先返回旧数据（最多再用 5 分钟）、后台重建。
- 「可用模型」列表（令牌、操练场等处）和后台定价问题检查改读内存渠道缓存（开启内存缓存时；内容和顺序与上次渠道缓存刷新时的数据库一致，渠道改动后几秒内随渠道缓存一起刷新；渠道因请求失败被自动禁用时，只由它提供的模型立即从列表里去掉，和选渠道一致），不再每个分组查一次数据库。
- 页面限流返回自动重试的提示页（第 4 条）；静态文件提供 brotli 版本（第 2 条）。
- 前端：首屏先显示启动外壳，不再长时间白屏（应用样式表放在启动外壳之后，样式文件很慢或卡住时外壳也照常显示，样式到了才渲染应用，不会出现无样式的页面）；老浏览器提示（第 8 条）；可选面板加载失败时就地显示错误，不再整页跳到错误页；使用日志加载失败时显示「加载日志失败」、原因和「重试」按钮，不再显示「未找到日志」（网络错误、代理返回的 502/503/504、429 和接口返回的失败原因都在这里显示，数据库出错时接口返回的也是失败原因；只有日志接口直接返回 HTTP 500 时，照旧在重试几次后跳到错误页）；返回的访客先看到上次缓存的首页自定义内容，再在后台刷新；设置了 `UMAMI_WEBSITE_ID` 时统计脚本改为 `async` 加载，统计站点很慢或连不上时不再拖住页面启动。
- 前端下载更少：生产构建的英文语言包去掉键和值相同的条目，KaTeX/marked 只在显示 Markdown 时下载，钱包页不再下载整个图标包。每个页面首次打开都要下载的入口脚本和样式从约 931 KB（gzip）降到约 657 KB（brotli）；首页、模型广场、登录页和控制台常用页首次打开下载的静态文件少约 29%–34%，钱包页少约 74%。数据看板的图表分块在悬停链接和概览页空闲时提前下载（概览页因此会在后台多下载约 0.5 MB），页面切换去掉了模糊滤镜，使用日志每行只解析一次附加信息。
- 构建：前端构建结束时多一步 brotli 预压缩（约 30 秒），`web/dist`、二进制和镜像各大约多 16 MB；Dockerfile 不变。
- 第二批（只改前端，没有新增环境变量）：系统设置的代码不再打进入口，模型广场的详情抽屉、性能图表和代码示例、渠道列表的弹窗和抽屉都在第一次打开时才下载（第六节），第一次打开时会先显示骨架屏或「加载中...」；构建版本号写进 `index.html`，只改了个别页面的发布之后老用户只需重新下载约 19 KB 的 runtime 和改动的页面；控制台页面共用的界面组件和数据表格各自成一个文件，切换控制台页面时不再重复下载。登录页和注册页显示 2 秒后、浏览器空闲时，提前下载登录后要进入的页面（`redirect` 指向的本站页面，没有就是数据看板）的代码，进入数据看板时登录页实测下载约 0.29 MB（7 个文件，实际传输的 brotli 字节），注册页约 0.25 MB（按构建产物计算，它自己已经下载了其中一个文件）；只下载代码，不发接口请求，省流量模式和 2G 网络下不下载。实测（2 Mbit/s 网络）在登录页停留约 5 秒再登录时，登录后不用再下载代码，从点击登录到数据看板显示约 0.35 秒（旧版本约 0.83 秒）；停留不到 2 秒就登录（例如浏览器已经自动填好密码），或在省流量模式、2G 网络下，这一步要先下载这些文件，约 1.7 秒（旧版本约 0.8 秒；2G 只模拟了浏览器报告的网络类型，网速仍是 2 Mbit/s，真实的 2G 网络下这一步会慢得多），不过直接打开的登录页本身快约 1.5 秒，从打开登录页算起仍比旧版本快约 0.5 秒（从首页点进登录页的情况没有实测）。模型广场和模型详情页加载价格失败时显示原因和「重试」，不再显示「总计 0 个」或「未找到模型」；价格接口出错时，用到价格的其他页面（使用日志、系统设置）也不再跳到错误页，只提示错误。`/api/status` 出错（例如 500、502）时，模型广场不再把访客送回首页，而是按浏览器里记下的上次站点状态决定能否进入，没有记下的就按默认设置（开放、不要求登录），数据照旧由后端检查权限。`/api/status` 卡住不返回时，以前模型广场一直出不来；现在整页打开要先等根路由的 8 秒、再等模型广场守卫的 3 秒（实测约 15 秒后显示列表），然后同样按上次的站点状态或默认设置决定，从站内其他页面进入模型广场时最多等守卫的 3 秒；记下的站点状态不会缩短这段等待。未登录访客打开模型广场时价格接口返回 401（要求登录）就转到登录页，返回 403（未开放）就回首页，不再先尝试刷新登录状态、弹「会话已过期！」；已登录用户的登录状态一时刷新不上（例如刷新接口限流或服务重启）时，模型广场显示加载失败和「重试」，已显示的列表保留，不会跳到登录页又被送回。按需加载的部分（模型广场的详情抽屉、性能图表和代码示例，渠道列表的弹窗和编辑抽屉）的某个文件第一次加载失败（例如网络中断）时，和路由页面一样整页自动刷新一次（每个标签页、每个文件一次；真断网时这次刷新会停在浏览器的断网提示页）；本标签页已经为这个文件自动刷新过、再次失败时才在原处处理：详情抽屉和性能图表显示「重试」，代码示例不提示、显示不带高亮的代码，渠道列表的弹窗和编辑抽屉关闭并弹出提示（提示里只有「刷新页面」）；之后再次打开（或点「重试」）会重新下载。

**回滚：** 没有数据库结构变化，新增的环境变量旧版本会忽略。只撤掉第二批时，把 `deploy/.env` 里的 `NEW_API_TAG` 改成第一批合并后的镜像 `52bd1eb`，再 `docker compose up -d`（见第四节），其他设置都不用动，「第二批」那一项的变化回到旧样子（例如 `/api/status` 出错时模型广场又把访客送回首页）。回到第一批之前的版本时，上面所有行为变化都回到旧样子（例如 Redis 出错时又返回 500，刷新登录状态又和登录共用额度），按上面几条改过的下面三项是第一批的设置，这时要一起改回：
- 升级后删掉的 `CRITICAL_RATE_LIMIT=60` 加回去：旧版本里刷新登录状态又和登录共用每 IP 20 次 / 20 分钟，同一 IP 约 20 次整页打开就会被退出登录。
- `REDIS_CONN_STRING` 去掉短超时：旧版本 Redis 一出错就返回 500，超时调短只会让 500 来得更快。
- 调低过的 `SQL_MAX_OPEN_CONNS` 改回原来的值：旧版本调低后，连接被占满时所有渠道选择都会排队等同一个查询。

---

## 二十一、使用日志：总数超过 1 万条时显示「10000+」

**用户看到什么：** 使用日志页（管理员看全部日志，普通用户看自己的日志）分页栏的「总计」，在符合条件的日志超过 1 万条时显示「10,000+」（千位分隔符跟浏览器的语言走）；手机端的页码显示成「1 / 500+」这样的下限（每页 20 条时）。1 万条及以下（含正好 1 万条）仍显示准确条数，翻页和以前完全一样。

**为什么：** 以前每次打开或翻页，服务器都要把符合条件的日志全部数一遍（普通用户是所选时间范围内自己的日志，管理员是所有人的日志）。日志表大了以后这一步要几百毫秒到几秒，并发时更慢；用户日志原来那个「最多数 1 万条」并没有生效（`LIMIT` 加在了计数结果那一行上）。现在最多只数 10001 条（`SELECT count(*) FROM (SELECT 1 FROM logs WHERE … LIMIT 10001)`），SQLite、MySQL、PostgreSQL 和 ClickHouse 日志库用的是同一条查询。本机约 30 万行日志的测试库上（MySQL 8.0，单个请求）：一个用户 30 天内约 15 万条日志，原来光计数就要 169 ms，现在计数加取一页共 14 ms；管理员看 30 天内的全部日志（约 30 万条），从 33 ms 降到 3 ms。筛选后不到 1 万条、但这个用户的日志很多时（例如只看错误日志），计数仍要把这个用户的日志过一遍，速度和以前一样。

**超过 1 万条以后怎么翻页：**
- 「下一页」照常可用，可以一直翻到 1 万条以后；翻回来的一页不满（例如每页 100 条只回来 37 条）就是最后一页，「下一页」变灰。
- 总页数不知道，所以这时「最后一页」按钮（»）不可用；页码里最后那个数字只是目前已知的页数（每页 100 条时一开始是 100，翻过去以后会跟着往后加）。
- 翻到很靠后（几百页以后）每页会比前面慢一些，数据库要跳过前面的行。
- 总条数正好是每页条数的整数倍时，在最后一页点「下一页」会取回一页空白，页面随即回到这最后一页，「下一页」变灰。
- 打开的页在最后一页之后（例如打开旧链接、在第 2000 页从「全部」切到「仅自己」、日志被清理以后），会回到第 1 页，列表超过 1 万条和不超过 1 万条（显示准确条数）时都一样。升级前遇到这种情况，标签页会卡住（不论列表有多少条），这次一并修好。
- 每页最多 100 条：链接里的 `pageSize` 超过 100 时按 100 条显示（服务器本来就最多返回 100 条）。
- 想看准确条数或更早的日志：缩小时间范围，或加上用户名、模型、令牌、类型等筛选条件，让结果不超过 1 万条。

**不变的：** 导出日志（导出全部符合条件的日志，不受 1 万条限制）、统计（额度、RPM、TPM）、任务日志和绘图日志的列表（它们和使用日志共用页面链接，只是链接里的 `pageSize` 同样最多按 100 条）。

**接口：** `GET /api/log/` 和 `GET /api/log/self` 的 `data` 里多了 `total_capped`（布尔值）。为 `true` 时 `total` 是 10000，意思是「至少 10000 条」；为 `false` 时 `total` 是准确条数。自己写脚本按 `total` 算总页数的，要先看这个字段（翻页参数 `p` 可以超过按 `total` 算出的页数）。`p` 或 `page_size` 小于 1 时按默认值（第 1 页、每页 10 条）；以前负数的 `page_size` 会让列表查询不带 `LIMIT`，一次返回全部符合条件的日志。

**升级与回滚：** 没有数据库结构变化，也没有新的设置项。回滚到旧版本即恢复原来的精确计数（以及原来的速度）。

---

## 二十二、重启或更新后保持用户登录

**是什么：** 以前每次重启或更新容器，所有用户（包括 root）都会被强制退出登录。原因是没有设置 `SESSION_SECRET` 时，登录用的签名密钥每次启动都随机生成，旧的登录凭证随之失效；本仓库的 `deploy/docker-compose.yml` 以前也没有传 `SESSION_SECRET`。现在新增开关「重启或更新后保持用户登录」，**默认开**：第一次启动时生成一把随机密钥（32 字节）保存在数据库里，之后每次启动都用这一把，重启和更新不再让用户退出登录。

**位置：** 系统设置 → 身份验证 → 基本身份验证 → 「重启或更新后保持用户登录」（第一项），只有 root 能看到和修改。**改动在下次重启时生效**（保存后当前运行不变），开关下方会提示下次重启会发生什么。

| 情况 | 下次重启时 |
|---|---|
| 开着（默认），没有设置 `SESSION_SECRET` | 用户保持登录 |
| 关着 | 所有用户都会被退出登录，之后每次重启都会；同时删除数据库里保存的密钥，以后再打开会换一把新密钥（打开后的第一次重启还会退出一次） |
| 设置了 `SESSION_SECRET` | 以 `SESSION_SECRET` 为准：只要它不变，用户保持登录。开关变灰，显示「由环境变量 SESSION_SECRET 固定，此开关不生效」；数据库里保存的密钥会被删除 |

**升级说明：**
- **第一次升级到这个版本时，所有用户还会被退出一次**（旧版本的随机密钥没有保存下来，没法沿用），之后的重启和更新不再退出。
- 数据库新增 `server_secrets` 表（启动时自动创建，只有一行 `session_secret`），没有其他结构变化。多节点时先升级主节点（只有主节点建表）。
- `deploy/docker-compose.yml` 现在透传 `SESSION_SECRET`（`deploy/.env` 里不写或留空就是没设置）。不用 `SESSION_SECRET`（默认）时服务器上的旧 compose 文件照常能用；要用它时，先按下面「设置或更换 `SESSION_SECRET`」在服务器的 compose 文件里加上那一行。原来在别处给容器设置过 `SESSION_SECRET` 的，把同一个值写进 `deploy/.env`，否则切换后所有用户要重新登录一次。
- 没有设置 `CRYPTO_SECRET` 时，加密密钥跟随登录签名密钥，所以开关开着时，发给 OpenAI 的用户安全标识（第十八节）和任务结果下载链接在重启后也保持不变。审计里的「输入内容指纹」（第十九节）例外：指纹对应的常常是输错位置的密码，密钥不能和指纹放在同一个数据库里，所以不用这把密钥；没有设置 `CRYPTO_SECRET` 和 `SESSION_SECRET` 时，指纹密钥照旧每次启动随机生成，重启前后的指纹对不上。

**想让所有人立即退出登录（例如怀疑登录被盗用）：**
- 只退出一次、之后照常保持登录：删除保存的密钥再重启，启动时会生成一把新的。三种数据库都是同一句：`DELETE FROM server_secrets WHERE name = 'session_secret';`，然后 `docker compose restart new-api`（多节点要重启所有节点）。
- 设置了 `SESSION_SECRET` 的：在 `deploy/.env` 里换一个新值，再执行 `docker compose up -d`。不能用 `docker compose restart`，它不会读新值（见下面「设置或更换 `SESSION_SECRET`」）。
- 关掉开关再重启也会让所有人退出，但之后每次重启都会退出，直到重新打开（重新打开后的第一次重启还会再退出一次）。

没有设置 `CRYPTO_SECRET` 时，加密密钥就是登录签名密钥，所以上面三种做法都会同时换掉加密密钥：之前发出的任务结果（视频、图片）下载链接失效（返回 404，客户端要重新查询任务拿新链接），发给 OpenAI 的用户安全标识也全部改变（第十八节）；开关关着时每次重启都是这样（和以前一样）。想让换登录密钥不影响这两样，事先在 `deploy/.env` 里设置一次 `CRYPTO_SECRET`（第十八节），再执行 `docker compose up -d`（compose 文件里要有 `CRYPTO_SECRET` 那一行，见下面「设置或更换 `SESSION_SECRET`」第 1 条）；第一次设置时这两样也会变一次，之后就不再跟着登录密钥变。

这三种做法都只让网页登录失效：用户创建的「访问令牌」和 `sk-` 令牌照常能用，改密码也不会让「访问令牌」失效。怀疑账号被盗用时，还要在这个账号里检查并删除可疑的「访问令牌」和 `sk-` 令牌（管理员也可以先禁用这个用户），并看看有没有多出来的管理员。

**设置或更换 `SESSION_SECRET`：**
1. 服务器上的 compose 文件里，`new-api` 的 `environment` 下要有 `- SESSION_SECRET=${SESSION_SECRET:-}` 这一行。按第三节只执行 `docker compose pull` 更新的服务器上还是旧文件，没有这一行，`.env` 里的值传不进容器，也没有任何提示：手动加上这一行。不要把整份文件换成仓库里的 `deploy/docker-compose.yml`：服务器上的这份可能按第二十节第 4 条改过（`stop_grace_period`、`SHUTDOWN_TIMEOUT_SECONDS`、日志上限、`TRUSTED_PROXIES` 等），整份覆盖会把这些改动丢掉。用 `git clone` 部署、没改过这个文件的，也可以在仓库目录里 `git pull`。要设置 `CRYPTO_SECRET` 的也一样，文件里要有 `- CRYPTO_SECRET=${CRYPTO_SECRET:-}`（2026-11-20 之前复制的 compose 文件里没有）。
2. 改好 `deploy/.env` 后执行 `docker compose up -d`，它会用新的 `.env` 重建容器（多节点要在每个节点上都做）。`docker compose restart` 不会重新读取 `.env`，容器里还是旧值，而且没有任何提示。
3. 确认生效：`docker compose logs new-api | grep SESSION_SECRET` 能看到 `SESSION_SECRET is set`（中文日志「已设置 SESSION_SECRET」）；设置页的开关变灰，显示「由环境变量 SESSION_SECRET 固定，此开关不生效」。换了新值时，之前登录的浏览器都要重新登录（可以事先在另一个浏览器里登录一个账号，换完后刷新它来确认）。

**安全提示：** 开关开着、又没有设置 `SESSION_SECRET` 时，登录签名密钥保存在数据库里。拿到数据库副本或备份的人，可以冒充当时已登录（30 天内登录、还没有退出）的任何用户，包括 root，直接进入控制台。没有设置 `CRYPTO_SECRET` 时，加密密钥也是这把密钥，所以这个人还能伪造任务结果（视频、图片）的下载链接；Redis 里登录会话和用户缓存的防篡改校验也用这把密钥：拿到数据库副本、又能改写 Redis 的人，可以伪造缓存，冒充任何用户，包括当时没有登录的 root（本仓库 compose 里的 Redis 没有对外端口）。设置一次 `CRYPTO_SECRET`（第十八节）后，下载链接和这个校验都不再用这把密钥。审计里的「输入内容指纹」不用这把密钥，拿到数据库副本也没法核对（第十九节）。数据库里本来就有渠道密钥和用户的 `sk-` 令牌，备份本来就要和数据库同样保护（不要放在公开的地方，存放和传输时加密）。新建密钥时写入的值也在数据库自己的日志里：MySQL 8 默认开着的 binlog、PostgreSQL 的 WAL（包括归档）里都有，要和备份一样保护。不想把这把密钥放进数据库，就在 `deploy/.env` 里设置 `SESSION_SECRET`（`openssl rand -hex 32` 生成，设好后不要再改；第一次设置会让所有用户退出一次），步骤和确认方法见上面「设置或更换 `SESSION_SECRET`」：只改 `.env` 再 `docker compose restart` 不会生效。备份可能泄露时，按上面的办法换一把新密钥。这把密钥不会出现在 new-api 的日志、接口（包括 `GET /api/option`）、审计和多节点的设置同步里；设置页只显示它来自哪里（环境变量、数据库或随机）。

**多节点：** 所有节点连同一个数据库时，自动共用数据库里的这一把密钥，在一个节点登录后换到其他节点也有效（以前没设 `SESSION_SECRET` 的多节点部署做不到）；几个节点同时第一次启动也只会生成一把。也可以给所有节点设置相同的 `SESSION_SECRET`，但不要一部分节点设置、一部分不设置。改了开关之后要把所有节点都重启。

**启动时读写数据库失败：** 服务照常启动，这次用随机密钥（和以前一样：之前登录的用户要重新登录，这次启动后登录的用户在下次重启后也要再登录一次）。日志里有一行 `keep users signed in after restarts: could not load the login signing key (…)`（`DEFAULT_LANGUAGE=zh-CN` 时是「重启或更新后保持用户登录：无法读取或保存登录签名密钥（…）…」）。这一行写在错误输出里，行首和其他系统日志一样是 `[SYS]`，没有 ERROR 字样，要按上面这段文字搜；括号里只有数据库的错误码，例如表还没建好时是 `mysql error 1146`、`postgres error SQLSTATE 42P01` 或 `sqlite error 1`。设置页的开关下方提示「本次启动没有使用保存的密钥」。数据库恢复后再重启一次即可。日志里不会打印密钥。

**回滚：** 旧版本不认识这个开关和 `server_secrets` 表，表留在库里没有影响。没有设置 `SESSION_SECRET` 时，回滚那一次和旧版本之后的每次重启都会让所有用户退出登录；设置了 `SESSION_SECRET` 时（服务器的 compose 文件里已有那一行，回滚只改 `NEW_API_TAG`），旧版本也用它，回滚和之后的重启都不会让用户退出。没有设置 `SESSION_SECRET` 时，之后再升级回来会继续用表里原来那把密钥：回滚之前登录、还没过期也没退出的会话会恢复有效；不希望这样，就在升级回来之前执行上面的 `DELETE`。从备份恢复数据库也是这样：备份时还有效的会话（登录后 30 天内、当时还没有退出）会和备份里的密钥一起回来（设置了 `SESSION_SECRET` 时，只要它没变也一样），备份之后做过的退出登录和吊销都会丢失。因为安全问题恢复备份时，恢复后按上面「想让所有人立即退出登录」的办法换一把新密钥。

**排查：** 重启后用户还是被退出：
1. 是不是第一次升级到这个版本（会退出一次）。
2. 看开关下方的提示：「已保存为关闭」→ 打开并保存，下次重启后生效；「由环境变量 SESSION_SECRET 固定」→ 确认每次启动 `deploy/.env` 里的 `SESSION_SECRET` 都没变；「本次启动没有使用保存的密钥」→ 在启动日志里找 `could not load the login signing key`（中文日志「无法读取或保存登录签名密钥」）那一行，看括号里的数据库错误码。
3. 启动日志：`docker compose logs new-api | grep -iE 'signing key|SESSION_SECRET'`（中文日志搜「登录签名密钥」或 `SESSION_SECRET`）。正常时是 `using the login signing key kept in the database`（「使用数据库里保存的登录签名密钥」），第一次是 `saved a new login signing key in the database`。
4. 查数据库里有没有这一行（只查这两列，不要查 `value`，也不要把它贴到任何地方）：`SELECT name, created_at FROM server_secrets;`。日志说密钥无效（`the kept login signing key is invalid`）时，执行上面的 `DELETE` 再重启。
5. 多节点：所有节点都已升级并重启；`SESSION_SECRET` 要么都设成同一个值，要么都不设。
