# 部署说明（定制版）

本仓库是 [new-api](https://github.com/QuantumNous/new-api) 的定制分支，改动集中在 Waffo Pancake 支付网关：结算币种改为 CNY、最低充值数量默认 50、手续费转嫁给用户。

镜像由 `.github/workflows/docker-ghcr.yml` 自动构建并发布到 GitHub Container Registry：

```
ghcr.io/henfailsf2/new-api-custom:latest      # 跟随最新一次成功构建
ghcr.io/henfailsf2/new-api-custom:<短 SHA>     # 锁定某次构建，例如 82a7684
```

多架构 `linux/amd64` + `linux/arm64`。包是公开的，拉取不需要登录。若日后把包改成私有，服务器上需要先登录（PAT 权限 `read:packages`）：

```bash
echo "<你的 PAT>" | docker login ghcr.io -u henfailsf2 --password-stdin
```

---

## 一、首次部署

数据库用的是外部 MySQL，容器里只跑应用和 Redis。

```bash
# 1. 拉代码（只需要 deploy 目录里的两个文件，也可以手动 scp 过去）
git clone -b claude/new-api-waffo-pancake-custom-bvh3x8 \
  https://github.com/henfailsf2/new-api-custom.git
cd new-api-custom/deploy

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

上游发新版时：

```bash
git remote add upstream https://github.com/QuantumNous/new-api.git   # 只需一次
git fetch upstream --tags

git checkout claude/new-api-waffo-pancake-custom-bvh3x8
git merge v1.0.0-rc.31        # 换成上游最新的 release tag，不要用 upstream/main
```

冲突大概率出现在这几个文件（就是定制改动所在的位置）：

| 文件 | 定制内容 |
|---|---|
| `service/waffo_pancake.go` | 三处 `USD` → `CNY` |
| `setting/payment_waffo_pancake.go` | `MinTopUp = 50`，三个手续费变量 |
| `controller/topup_waffo_pancake.go` | `getWaffoPancakePayMoney` 里的反推逻辑 |
| `model/option.go` | 三个手续费键的注册与解析 |
| `web/src/features/system-settings/integrations/*` | 「计价与手续费」UI |
| `web/src/i18n/locales/*.json` | 9 条新文案 |

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
git push origin claude/new-api-waffo-pancake-custom-bvh3x8
```

在 [Actions 页面](https://github.com/henfailsf2/new-api-custom/actions) 等构建完成（约 7 分钟），然后在服务器上更新：

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

历史 tag 在 [Packages 页面](https://github.com/users/henfailsf2/packages/container/package/new-api-custom) 能查到。

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
2. **数据库 schema 不会自动回退**。当前定制版基于 `v1.0.0-rc.30`，和官方 `latest` 是同一个上游版本，schema 一致，回滚没问题。但如果你已经合并过更新的上游版本再回滚到旧官方镜像，新增的列/索引会留在库里——通常是加法式变更，旧版本能正常跑；万一起不来，用切换前的 `mysqldump` 恢复。

---

## 五、故障排查

| 现象 | 排查方向 |
|---|---|
| `docker pull` 报 `denied` | GHCR 包被设成私有了，去 Packages 设置改回 Public，或按开头的命令 `docker login` |
| 容器起来就退出 | `docker compose logs new-api`，八成是 `SQL_DSN` 写错或 MySQL 不通 |
| 最低充值数量还是 1 | 数据库 `options` 表里有旧行，用上面那条 SQL 查，`DELETE` 掉再重启，或直接在后台改 |
| 支付金额不对 | 后台看「计价与手续费」四个值；反推公式是 `实付 = (标价 + 固定费) / (1 - 费率)`，50 元档应为 55.78 |
| 前端页面白屏 | 强刷清缓存；仍然白屏看 `docker compose logs`，可能是构建时前端产物没打进去 |
