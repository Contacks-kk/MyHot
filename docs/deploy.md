# 部署

生产运行时由 React Router SSR Web、Go API、Go Worker/River 和 PostgreSQL 组成。浏览器与 SSR 的业务请求都经 Web 同源代理访问 Go；公网只暴露 Web。TypeScript 用于前端、HTTP 契约及迁移工具；旧 Fastify API/Worker 与 TypeScript 业务实现已退役。

## Docker Compose

需要 Docker Compose。从仓库根目录使用 `deploy/compose.yaml`：`deploy/Dockerfile.web` 构建 Web，`backend-go/Dockerfile` 构建 Go API/Worker，`deploy/Dockerfile.setup` 构建一次性 SQL 迁移工具。长期服务为 db、go-api、go-worker、web 和可选 caddy；setup 完成后退出。数据库与上传数据保存在命名卷中。

~~~bash
cp .env.example .env
docker compose --project-directory . --env-file .env -f deploy/compose.yaml up -d --build
docker compose --project-directory . --env-file .env -f deploy/compose.yaml ps
~~~

首次运行前填写 SITE_URL、ADMIN_PASSWORD、SESSION_SECRET、IMG_PROXY_SIGN_SECRET 和 POSTGRES_PASSWORD，并确保 `REDIS_URL`（Compose 默认指向内置 `redis` 服务）可用。用户开始保存自己的模型 API Key 前，需配置 `USER_API_KEY_ENCRYPTION_KEY`。Web 使用 GO_API_BASE_URL 连接 Go；Go 的 River 使用独立 pgx 连接池，应用 Repository 使用独立 GORM/database/sql 连接池，Redis 承担用户会话与实时行情缓存。SQL 迁移仍由 tooling/scripts/migrate.ts 执行，Go 不调用 AutoMigrate。

Compose 只向宿主机发布 Web 端口。Go API、Worker 与数据库留在内部网络；Web 代理是 Go API 的受信代理，生产 Compose 已启用 GO_TRUST_PROXY=true。Go Worker 与 Web 共享 og-cache 用于分享图缓存和预热；上传、附件和备份由 Go API/Worker 访问 data 卷，Web 不挂载该卷。

## 从旧版本切换

第一次从带 TypeScript API/Worker 的版本升级时，先备份数据库、上传数据和用户密钥配置。用旧版本的 Compose 文件停止旧进程并等待在途任务结束，再用当前 Compose 文件部署。由于当前 Compose 已删除 api、worker 服务，停止命令必须使用旧版本 Compose 配置：

~~~bash
docker compose -f <旧版本中的 Compose 文件> stop worker api
docker compose --project-directory . --env-file .env -f deploy/compose.yaml up -d --build --remove-orphans
~~~

确认旧容器已停止后再启动新 Go Worker，不能让旧、新消费者并行处理同一种任务。此升级不会删除 pg-boss schema、任务记录或 River 状态，也不会删除业务数据。Go Worker 的 ops.queue.handoff 在启动时并每分钟扫描可迁移的旧任务；它在同一 PostgreSQL 事务中写入 River 并完成对应 pg-boss 记录。分析、正文和归组任务由数据库状态扫描器重建；不符合当前凭据或安全阀条件的公众号手动检查、事件综述仍留在 pg-boss，等条件满足后再导入。保留 job_runs 和 fetch_runs 审计。

Go API 与 Worker 启动时都可确保 River schema 已升级，二者共用 PostgreSQL advisory lock 串行执行 River 自身迁移。应用数据库 schema 仍只由 setup 的 SQL 迁移器负责。管理员操作在处理其他安全阀关闭时仍可提交并记录待处理 River 工作；采集、模型、投递和媒体消费者只在对应开关开启后运行。`COLLECT_ENABLED=false` 时 Go Worker 不注册 RSS 消费者，个人“测试抓取”接口返回采集已关闭且不入队；之前已排入队列的抓取任务保持待处理。

用户信源迁移完成后，旧信源不会自动归入任何账号。迁移后可用 `tooling/scripts/retire-unsubscribed-sources.ts` 分批暂停没有有效用户订阅的旧信源；它默认只预览，不加载环境文件，每批最多处理 500 条。明确指定目标数据库后先预览，再加 `--apply` 执行。这个转换只更新信源启用状态和下次抓取时间，保留信源、文章及抓取历史。用户添加相同 RSS 地址时会复用旧信源并按当前订阅恢复抓取。

~~~bash
DATABASE_URL='postgres://127.0.0.1:5432/myhot' npm run sources:retire-unsubscribed
DATABASE_URL='postgres://127.0.0.1:5432/myhot' npm run sources:retire-unsubscribed -- --apply
~~~

pg-boss 队列导入完成且确认没有未处理任务后，才可在后续单独发布中退役 pg-boss schema。当前版本刻意保留该状态，避免迁移前置部署误删待处理任务；不能手工清空或删除 pg-boss 表来跳过导入。Go API/Worker 不再创建 TypeScript 服务，也不要求安装旧 API/Worker 镜像。

## 注册、采集和模型开关

注册默认关闭。确认条款与隐私内容并配置可用 SMTP 后，可设置 `OPEN_REGISTRATION_ENABLED=true` 开放邮箱注册。注册和邮箱验证不依赖用户模型 API Key 的加密配置；用户以后保存自己的模型 API Key 时才需要该密钥。加密密钥必须是 Base64 编码的 32 字节值；若数据库已有用户模型密钥密文，必须恢复原密钥，不能新生成一个替换它。只有确认当前数据库没有历史密文时，才为新环境生成一次并持久备份：

~~~dotenv
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USERNAME=mailer@example.com
SMTP_PASSWORD=<邮件服务凭据>
SMTP_FROM=MyHOT <mailer@example.com>
OPEN_REGISTRATION_ENABLED=true
~~~

只有需要启用用户模型配置时，才在同一运行环境配置 `USER_API_KEY_ENCRYPTION_KEY=<原有密钥或新环境首次生成的密钥>`。

所有安全阀只认字面值 true，示例配置默认关闭。开发和验收环境保持外发、采集及模型调用关闭：

~~~dotenv
COLLECT_ENABLED=false
MODEL_CALLS_ENABLED=false
PERSONALIZATION_ENABLED=false
GO_SHARED_BODY_ENABLED=false
GO_STORY_DIGEST_ENABLED=false
GO_CONTENT_DELIVERY_ENABLED=false
GO_SHARED_PIPELINE_ENABLED=false
GO_MEDIA_PREPARE_ENABLED=false
FEISHU_INTERNAL_ENABLED=false
FEISHU_CONTENT_PUSH_ENABLED=false
INDEXNOW_SUBMIT_ENABLED=false
~~~

日常内容读取不会调用模型。注册启用后，用户可在个人设置配置自己的模型和关注项；个人加工由 PERSONALIZATION_ENABLED 控制。公共采集由 COLLECT_ENABLED 控制。共享文章分析、归组、发布及相关队列切换由 GO_SHARED_PIPELINE_ENABLED 控制，它还要求模型、正文、采集、投递和媒体安全阀同时开启；只有确认旧写入方已停止、pg-boss 待处理通知/媒体已导入 River 后才能启用。启用后 Go 会从业务状态恢复未完成文章和归组任务。不要为了普通 Web/API 启动而打开这些开关。

图片代理由 Go API 提供。只有 GO_MEDIA_PREPARE_ENABLED=true 才会排入 URL/模式媒体准备任务；该开关默认关闭。Feishu、IndexNow、模型和采集同样必须由部署者按需显式打开。

## 本机开发（Windows）

需要 Node.js 24.11 以上和 Go 1.25.1。开发配置使用仓库根目录未提交的 `.env.development`，部署仍使用部署主机自己的 `.env`（由 `.env.example` 初始化）。复制 `.env.development.example` 为 `.env.development`，填写开发数据库地址、账号和 URL 编码后的密码。

~~~dotenv
DATABASE_URL=postgres://<DB_USER>:<URL_ENCODED_DB_PASSWORD>@127.0.0.1:5432/myhot_test
MYHOT_TEST_DATABASE_URL=
MYHOT_TEST_REDIS_URL=
REDIS_URL=redis://127.0.0.1:6379/0
GO_API_BASE_URL=http://127.0.0.1:3711
GO_API_ADDR=127.0.0.1:3711
GO_TRUST_PROXY=true
PUBLIC_BASE_URL=http://localhost:3611
COLLECT_ENABLED=false
MODEL_CALLS_ENABLED=false
PERSONALIZATION_ENABLED=false
~~~

启动完整本地栈（Web、Go API、Go Worker）：

~~~powershell
npm ci
npm run dev:local
~~~

访问 `http://localhost:3611`，Go API 本机监听 `127.0.0.1:3711`。`dev:local` 会为这三个进程覆盖开发环境中的相应地址配置。按 Ctrl+C 会停止三个进程。Worker 也会连接该测试库，启动时会初始化 River 并扫描旧队列；如需执行集成测试，应使用单独的本机 `_test`/`_ci` 数据库设置 `MYHOT_TEST_DATABASE_URL`，不要复用共享的 `aihot_v2_test`。

连接共享开发数据库时，使用部署者提供的连接参数和受限访问方式。集成测试使用独立的本机测试库。

需要单独迁移时，先确认目标库和待执行迁移，再运行：

~~~powershell
node --env-file=.env.development tooling/scripts/migrate.ts
npm run build -w @aihot/web
~~~

开发免登录管理员仅可在非生产环境临时设置 DEV_AUTH_ROLE=admin；用户接口仍要求有效用户会话。

## 用户会话迁移至 Redis（0112）

用户会话（`myhot_session` Cookie + CSRF）已从 PostgreSQL `user_sessions` 迁移至纯 Redis 存储（30 天有效期，剩余不足 15 天时按请求节流滑动续期）。`0112_drop_user_sessions.sql` 会在停服迁移阶段删除 `public.user_sessions`。

- **对使用者影响**：切换上线后所有已登录用户的旧会话立即失效，需重新登录一次；用户账号、信源订阅、模型配置、关注项、个人热点和日报数据完全保留。
- **回退边界**：一旦执行 `0112` 删表后需回退到旧版本镜像，请使用 `deploy.sh` 备份阶段生成的 PostgreSQL 备份恢复后再启动旧版服务。

## 域名与缓存

可用 Caddy profile 自动管理 HTTPS：

~~~dotenv
SITE_URL=https://example.com
SITE_DOMAIN=example.com
PORT=127.0.0.1:3000
TRUST_PROXY=true
~~~

~~~bash
docker compose --project-directory . --env-file .env -f deploy/compose.yaml --profile https up -d --build
~~~

也可由 Nginx 转发到 Web，传递真实 Host 和协议，并覆盖客户端转发头。个人页面、API、RSS、Agent Markdown 和 MCP 内容要求已验证用户会话并使用私有/不缓存响应。品牌图标、robots、Manifest、security.txt 是 Web 提供的匿名静态资源；动态业务和公开内容 API 由 Go 提供。

## 更新与迁移

服务器上的 `/opt/aihot/deploy.sh` 是独立入口，不会随应用代码 push 自动更新。所有分支的 push 都运行 Check 工作流；先等待目标提交的 `check` 和 `docker` 两个任务成功，再部署。服务器只接受配置分支对应的完整 SHA、该分支的最新 push 检查及两个成功任务，不接受其他提交、PR 检查或较旧的成功记录。检查未创建、未完成、失败或取消时立即退出，并给出检查链接。

首次安装或更新入口时，从本机已经通过 CI 的完整提交导出脚本，通过 SSH 上传，不依赖服务器访问 `github.com` 执行 Git clone。在本机仓库 PowerShell 中执行：

~~~powershell
$revision = '<已通过 CI 的完整提交 SHA>'
$entry = Join-Path $env:TEMP "myhot-deploy-$revision.sh"
$content = git show "${revision}:deploy/deploy.sh"
if ($LASTEXITCODE -ne 0) { throw '无法从指定提交读取部署脚本' }
[System.IO.File]::WriteAllText($entry, ($content -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))
(Get-FileHash -Algorithm SHA256 -LiteralPath $entry).Hash.ToLowerInvariant()
scp $entry root@<服务器IP>:/opt/aihot/deploy.sh.upload
~~~

在服务器核对上一步的 SHA256，检查 Bash 语法，取得部署锁后备份并原子替换。此步骤只安装入口，不发布应用，也不覆盖服务器配置：

~~~bash
(
  set -e
  cd /opt/aihot
  expected_sha='<本机输出的 SHA256>'
  printf '%s  %s\n' "$expected_sha" deploy.sh.upload | sha256sum --check -
  bash -n deploy.sh.upload
  exec 9>.deploy.lock
  flock -n 9
  if [[ -f deploy.sh ]]; then
    cp -p deploy.sh "deploy.sh.backup-$(date +%Y%m%d%H%M%S)"
  fi
  install -m 0755 deploy.sh.upload deploy.sh.new
  mv -f -- deploy.sh.new deploy.sh
)
~~~

保留服务器上的 `/opt/aihot/.env`、`deploy.conf` 和 `compose.server.yaml`，不要从仓库覆盖已存在的文件。仓库的 `deploy/server-overlay.yaml` 仅作为首次安装的模板；部署脚本固定读取服务器根目录的 `compose.server.yaml`。从旧版本升级时，删除 `.env` 中指向 `shared/` 的配置路径，或改为新路径（行业和模型配置在 `/app/backend-go/config/`，站点联系图片在 `/app/frontend/brand/contact/`）；部署预检会在停止服务前拒绝残留路径。确认 `deploy.conf` 中的 `DEPLOY_REPOSITORY`、`DEPLOY_BRANCH` 已填写，`.env` 与服务器 Compose 覆盖配置仍指向现有数据库、数据卷和域名后，先运行预检：

~~~bash
cd /opt/aihot
./deploy.sh --check
~~~

`--check` 会解析分支、锁定提交、验证 CI、下载并检查源码和服务器配置；只保留日志及待检源码，不构建镜像、不停止业务容器、不备份或迁移数据库。通过不代表镜像构建已经成功。确认检查结果后，由你单独运行 `./deploy.sh` 正式发布；该命令重新解析并验证当时的分支提交，日志中的完整 SHA 是本次发布依据。

GitHub 公共仓库通过 `api.github.com` 查询分支及 CI，再通过 `codeload.github.com` 下载固定提交，不需要服务器安装 Git 或 Git 辅助镜像。不新增 GitHub 凭据。每次 API 请求总预算 60 秒、源码下载总预算 20 分钟，各最多尝试三次；仅临时网络错误、429 和临时服务错误重试。分支或权限错误、无效响应和失败 CI 立即退出，不自动切换来源、分支或旧版本。其他 Git 来源保留原有获取方式，不纳入 GitHub CI 门槛。

日志记录服务器配置路径、部署入口 SHA256、仓库、分支、完整提交和阶段耗时。源码中的部署入口与当前脚本不同时，会提示已下载脚本的路径，供审核并按上面的安装步骤更新；不会自更新。源码、检查或构建失败发生在停服前。备份阶段失败时 Worker 可能已经停止；迁移及之后的失败不自动回滚数据库，按日志中的备份和旧镜像信息恢复。成功发布前还会核对三个应用容器的运行镜像 ID。

升级前备份数据库、data 卷和用户 API Key 加密密钥。已有服务器升级统一使用上述入口，首次安装的 Compose 操作见前文，不用手工 `up --build` 绕过发布检查。

setup 自动应用新 SQL 迁移并退出。应用表结构来源于 `database/migrations/` 和 `backend-go/modules/<模块>/migrations/`；不修改已发布迁移、不做自动改表或历史数据回填。已迁移的模块 SQL 保留原文件名和内容，数据库仍按原文件名记账。Go 与 River 都不替代该迁移工具。当前分离版本不会删除业务表、pg-boss 状态、用户结果或运行审计。

上线关注标签时，先由 setup 应用 `0101` 至 `0107`，再用同一版本重启 Go API、Go Worker 与 Web。Worker 启动后导入内置目录，并分批建立近期文章与标签的关联。覆盖前用户端显示「暂无统计」。旧标签表和个人摘要继续保留。模型发现与语义复核使用现有平台初筛模型和调用回执，受 `MODEL_CALLS_ENABLED` 控制。开模型前用已标注样本检查晋升门槛，启用后从后台标签页和 `label.*` 审计记录观察候选积压、合并及撤下情况。

Go Worker/River 负责采集、个人任务、共享内容链、报告、翻译、运维、反馈转发、投递和媒体工作。周期语义按北京时间与原任务保持：报告每半小时检查并启动补跑；日报 09:00；信源图标 04:40；IndexNow 05:50；信源健康周报周一 09:00；故事关系每小时第 12 分钟；热度快照每小时第 02 分钟；热点榜单每 5 分钟；告警和恢复每 10 分钟。采集、报告、通知和个人任务仍受相应安全阀控制，具体启用条件以 .env.example 和 Go 配置校验为准。

## 备份与运行检查

配置对象存储凭证后 Go Worker 可执行备份；也可导出数据库：

~~~bash
docker compose --project-directory . --env-file .env -f deploy/compose.yaml exec -T db pg_dump -U aihot -Fc aihot > myhot.dump
~~~

还应保存 data 卷和用户密钥加密配置。Windows PowerShell 5.1 会改变二进制重定向，建议先在容器内生成 dump，再用 docker compose --project-directory . --env-file .env -f deploy/compose.yaml cp 复制。恢复到空数据库后再启动同版服务。docker compose --project-directory . --env-file .env -f deploy/compose.yaml down 保留卷，down -v 会删除数据库和附件。

~~~bash
docker compose --project-directory . --env-file .env -f deploy/compose.yaml logs -f --tail 100 go-api go-worker web
docker compose --project-directory . --env-file .env -f deploy/compose.yaml ps
~~~

/api/health 验证服务、PostgreSQL 与 Redis 连通。业务 smoke 默认检查注册状态与匿名访问边界；可用 Cookie 文件验证登录后的个人 API 和 SSR。Cookie 文件一行写入 myhot_session=<会话值>，脚本不会输出其内容：

~~~powershell
node tooling/scripts/smoke.ts --base http://localhost:3000
node tooling/scripts/smoke.ts --base http://localhost:3000 --cookie-file C:\Temp\myhot-cookie.txt
~~~

开放注册环境额外传入 --registration-open。

## 本机验证

项目验收命令如下；集成测试仅连接本机假服务和名称以 _test 或 _ci 结尾的数据库：

~~~powershell
npm run typecheck
$env:DATABASE_URL='postgres://myhot:<测试数据库密码>@127.0.0.1:5432/myhot_test'
$env:MYHOT_TEST_DATABASE_URL='postgres://myhot:<测试数据库密码>@127.0.0.1:5432/myhot_test'
$env:MYHOT_TEST_REDIS_URL='redis://127.0.0.1:6379/15'
npm test
npm run build -w @aihot/web
node --test frontend/tests/*.test.ts
go -C backend-go test ./...
go -C backend-go vet ./...
~~~

数据库账号需要有建库权限。Go 数据库与 Redis 集成用例分别通过 `MYHOT_TEST_DATABASE_URL` 和 `MYHOT_TEST_REDIS_URL` 启用。完整验收还应运行匿名及带登录 Cookie 的 smoke，并分别构建 Web 和 Go 镜像；没有本机 PostgreSQL/Redis/Docker 时，应将这些项记录为未运行，不能用静态检查代替。


## 邮箱验证码注册升级（0083 / 0084）

注册改为6位邮箱验证码，有效期和发送冷却均为60秒。API 内存缓存和发送限额在重启后清空；部署维持单个 Go API 实例。用户不需要重新注册；未验证账号可以重新获取验证码验证。历史注册验证链接失效，密码重置链接保留。

升级前停止旧 API，防止继续生成旧验证令牌。在执行 setup / SQL 迁移前，使用显式指定的数据库连接运行分批清理工具（默认只读统计）：

```powershell
node --env-file=.env.development tooling/scripts/retire-registration-tokens.ts
node --env-file=.env.development tooling/scripts/retire-registration-tokens.ts --apply
```

生产环境使用实际运行环境的 DATABASE_URL（Compose 中应使用能连接 db 服务的 setup 运行环境），不要沿用上述开发配置。清理只删除 `user_email_tokens.purpose='verify_email'` 的旧验证令牌，不删除账号、个人结果或 `password_reset` 令牌。确认清理完成后按现有部署步骤执行迁移和启动；0083添加仅允许密码重置令牌的 NOT VALID 约束，0084验证约束。不要修改历史迁移或绕过迁移检查。
