# Fork 仓库 Git 工作流方案

> 对应 Linear issue: [COD-9 创建适合的工作流](https://linear.app/codzen/issue/COD-9/创建适合的工作流)
> 状态：待执行（含一次性历史对齐，需人工确认）

## 1. 背景与约束

- 本仓库是 `QuantumNous/new-api` 的 fork（`origin` = `codzen-ai/new-api`，`upstream` = `QuantumNous/new-api`）。
- 存在注定只留在 fork 中的定制功能，同时需要经常性同步上游。
- 有两个环境：`production` 与 `staging`。
- 本地无法方便地启动实例，验证只能依赖 staging 环境。

## 2. 现状诊断（核实于 2026-08-02）

| 分支 | 上游基线 | 定制提交数 | 落后 upstream |
|---|---|---|---|
| `main` | — | 0（纯镜像，fetch 后为 `0ab020206`） | 0 |
| `staging` | `0f9f668c6` | 9 | 9 |
| `production` | `9a2d66031` | 7 | 67 |
| `origin/main` | `df4319360` | — | 极度陈旧（fork 初期） |

核心问题：**`production` 与 `staging` 是两条各自独立 rebase 的定制历史。**

- `git merge-base --is-ancestor production staging` → NO，两者互不包含。
- 同一批定制功能在两条分支上是不同的 SHA。`git cherry` 显示 production 的
  `feat: Implement log csv export` / `feat: Add export button` 在 staging 上已被
  `46131370c`（新前端版本）重做取代。
- 两分支 tree diff 已达 2041 个文件——上游把 `web/default/` 整体搬成了 `web/`。

结论：当前没有任何安全的方式把 staging 提升到 production。merge 会产生巨量冲突，
cherry-pick 会产生重复提交，只能靠人工搬运。这是本 issue 要解决的根因。

次要问题：

- 本地 `main` 曾陈旧 67 个提交，`origin/main` 停留在 fork 初期，容易误判基线。
- 定制提交里混杂三类内容（业务定制 / CI 基础设施 / 本地开发资产如 `PLAN.md`、`.claude/`），
  混在同一段 rebase 链中会放大冲突面。

## 3. 目标方案：单轨定制历史 + production 作为「已验证指针」

**只维护一条定制历史（`staging`）；`production` 永远是 `staging` 上某个已验证 commit 的
同一个 SHA，绝不独立 rebase。**

```
upstream/main ──► main（只读镜像，永不改动）
                   │
                   └─ rebase ─► staging   ← 唯一定制历史，唯一开发/验证入口
                                   │
                                   └─ git branch -f production <verified-sha>
                                      production 恒为 staging 的祖先
```

### 3.1 四条规则

1. **`main` 只做上游镜像。** 用 `git fetch upstream main:main` 更新，从不 checkout、
   从不在其上提交。同步 `origin/main` 或直接删除该远端分支，避免陈旧基线造成误判。
2. **同步上游 = 只 rebase `staging` 一次。**
   `git rebase main staging` → `git push --force-with-lease origin staging`。
   冲突在整个流程中只解决一次。
3. **发布 = 移动指针，不是 rebase。** staging 在 staging 环境验证通过后：
   `git branch -f production <staging-sha>` + force push，并打 tag `prod-YYYYMMDD`。
   回滚 = 把 `production` 指回上一个 tag，秒级且结果确定。
4. **新功能走 `feat/*` 分支**（基于 `main`），验证前 rebase 进 `staging`。
   可回馈上游的功能保持独立 feat 分支以便提 PR；fork-only 定制直接落 `staging`。

### 3.2 配套约定

- **启用 rerere**：`git config rerere.enabled true`。反复 rebase 同一批定制时冲突解法
  自动复用，是 fork 长期维护收益最高的一个开关。
- **定制提交固定分层顺序**（rebase 时按此排列，自下而上）：
  1. 可上游化的通用功能
  2. fork-only 业务定制
  3. 基础设施 / CI
  4. 本地开发资产（`PLAN.md`、`.claude/` 等）

  层次固定后，每次 rebase 的冲突面稳定、可预期。

### 3.3 CI / 自动部署到 staging 环境

因为本地无法方便启动实例，**staging 必须做到「push 即部署」**：任何进入 `staging`
分支的提交（直接 push 或 feat 分支合并）都自动构建镜像并发布到 staging 环境，
无需任何手工步骤。这是整个工作流能否成立的前提。

现状：`docker-ghcr.yml` 只监听 `production`，且仓库内没有任何 CD 机制，staging 目前
需要手工上机部署。

#### 第一步：构建（确定要做）

改造 `.github/workflows/docker-ghcr.yml`，让它同时监听 `staging`，并按分支决定 tag：

```yaml
on:
  push:
    branches: [production, staging]
  workflow_dispatch:

# tags 按分支区分：
#   staging    → ghcr.io/<repo>:staging     + :sha-<sha>
#   production → ghcr.io/<repo>:production  + :sha-<sha>
```

`:sha-<sha>` 始终一起推送，保证任何一次部署都能精确定位到 commit，便于回滚与排障。

#### 第二步：发布 —— 触发 `codzen-ai/codzen-deployment`

部署已有独立仓库 `codzen-ai/codzen-deployment`（本地 `~/Documents/Dev/Business/Codzen/codzen-deployment`）
承载，架构为 Traefik + Docker provider + `docker-rollout` 零停机，服务器目录 `/opt/codzen`。
它的 `deploy-to-staging.yml` / `deploy-to-prod.yml` 已支持 `repository_dispatch`
（event type `deploy-staging` / `deploy-production`），payload 为 `{service, tag}`，
会自动改写服务器 `.env` 的 `NEW_API_TAG` 并执行 `docker rollout new-api`。

**因此 new-api 仓库不自己 SSH**——SSH 凭据、compose 结构、Traefik 路由都归部署仓库管，
new-api 只负责「构建镜像 + 触发部署 + 等结果」。这样凭据只有一份，零停机与健康检查
复用部署仓库既有能力（compose 里 `new-api` 已配置 `/api/status` healthcheck，
`docker rollout` 会等容器 healthy 才切流量）。

**已实现的 workflow**（`.github/workflows/docker-ghcr.yml`，actionlint 通过）

- `docker` job：`branches: [production, staging]`，tag 用 `${{ github.ref_name }}`
  自动区分分支，并始终附带 `:sha-<sha>`。
- `deploy-staging` job：**仅 `staging` 分支触发**，`gh api .../dispatches` 发送
  `{"service":"new-api","tag":"sha-<sha>"}`，然后**轮询部署仓库的 run 状态**
  （最长 10 分钟），失败或超时则红灯。
- **`production` 保持只发布镜像、不自动部署**（维持现状）：生产发布仍由人在
  `codzen-deployment` 里手动运行 `Deploy to Production` 并填入 `sha-<sha>`。
  - `repository_dispatch` 是 fire-and-forget，不轮询的话部署挂了本仓库仍然绿灯——
    这正是「push 即静默挂掉」的陷阱，必须堵住。
  - 轮询用 `createdAt >= 触发时刻` 过滤，避免误读上一次的 run。

**镜像仓库：继续用 GHCR。** ECS 在阿里云香港地域，出境无墙，拉取 `ghcr.io` 无障碍，
不引入阿里云 ACR。部署仓库的 workflow 已在服务器上做 `docker login ghcr.io`。

**部署按 `:sha-<sha>` 而非分支 tag 落地**（部署仓库 compose 用
`ghcr.io/codzen-ai/new-api:${NEW_API_TAG:-production}`），这样「当前 staging 跑的是
哪个 commit」永远确定，回滚只需重新 dispatch 上一个 sha。

**需要在 new-api 仓库配置的 Secret**

| Secret | 用途 |
|---|---|
| `DEPLOY_PAT` | 触发并读取 `codzen-ai/codzen-deployment` 的 workflow，需要 `repo` + `actions:read` |

推送 GHCR 用内置 `GITHUB_TOKEN`，无需额外 Secret。仓库里已有的 `SSH_PRIVATE_KEY`
在新方案下不再需要，可以删除。

**两条硬性要求**

1. **部署失败必须让 workflow 红灯**。否则「push 即部署」会变成「push 即静默挂掉」，
   而你没有本地环境可以复现。
2. **staging 与 production 必须使用同一个 Dockerfile 与同一套构建参数**，只有 tag 不同。
   否则 staging 验证过的东西在 production 上不成立，整个单轨方案就失去意义。

> 注意：仓库根目录的 `docker-compose.yml` 是上游文件（硬编码 `calciumion/new-api:latest`），
> 与实际部署无关，不要改它。实际 compose 在部署仓库的 `staging/` 与 `production/` 下。

#### 与发布流程的衔接

```
push/merge → staging ─► GHCR :staging + :sha-x ─► dispatch deploy-staging
                                                        │
                                        codzen-deployment: docker rollout new-api
                                                        │
                                                   人工验证通过
                                                        │
                   git branch -f production <same-sha> ─► GHCR :production + :sha-x
                                                        │
                              人工在 codzen-deployment 运行 Deploy to Production
                                          （service=new-api, tag=sha-x）
```

生产侧保留两道人工闸门：移动 `production` 指针、手动运行部署 workflow。
但部署的镜像与 staging 验证过的是同一个 commit、同一份构建，风险已在前面消化。

## 4. 为什么不选 merge 流

保持 fork 用 `git merge upstream/main` 的好处是无需 force push、历史稳定。但：

- `AGENTS.md` 中有向上游提 PR 的规范，merge 流会让定制提交埋在 merge 泡中，提 PR 极为困难。
- 67 个上游提交产生的 merge commit 会让 `git log` 不可读。

综合判断：rebase 流 + rerere 更适合本项目。

## 5. 实施步骤

> 进度（2026-08-02）：阶段 0 已完成；阶段 1 的 rebase、构建验证与 force-push 已完成，
> 待 staging 环境人工验证后再做步骤 8；阶段 2 的 workflow 已提交，`DEPLOY_PAT` 已配置，
> 首次 `repository_dispatch` 链路验证中。

### 阶段 0 · 准备（无风险，可立即执行）— ✅ 已完成

1. 启用 rerere，让后续所有 rebase 复用冲突解法：
   ```bash
   git config rerere.enabled true
   ```
2. 把本地 `main` 对齐到 `upstream/main`：
   ```bash
   git fetch upstream --prune
   git fetch upstream main:main
   ```
3. 处理陈旧的 `origin/main`（停在 fork 初期 `df4319360`）：强推同步到 `upstream/main`，
   或直接删除该远端分支，避免后续误判基线。
4. 打安全网 tag —— **这是第 8 步唯一的后悔药**：
   ```bash
   git tag backup/production-$(date +%Y%m%d) production
   git push origin backup/production-$(date +%Y%m%d)
   ```

### 阶段 1 · 一次性历史对齐（含不可逆操作）

5. 把 `staging` 的 9 个定制提交追到最新 upstream（当前落后 9 个提交）：
   ```bash
   git switch staging && git rebase main
   ```
   冲突预期集中在 `model/log.go`、`controller/log.go`、`web/src/features/usage-logs/`。
6. 本地验证构建：
   ```bash
   go build ./...
   cd relaykit && GOWORK=off go build ./... && cd ..
   cd web && bun run build && cd ..
   ```
7. 推送并在 staging 环境**人工验证定制功能**（日志导出 CSV、缓存费用列、分组模型倍率）：
   ```bash
   git push --force-with-lease origin staging
   ```
8. 验证通过后执行对齐（**不可逆，需显式确认**，此步会丢弃 `production` 现有历史）：
   ```bash
   git branch -f production staging
   git tag prod-$(date +%Y%m%d) production
   git push --force-with-lease origin production
   git push origin --tags
   ```
   此后 `production` 恒为 `staging` 的祖先，两条历史合一。

### 阶段 2 · staging 自动发布（见 §3.3）

9. ✅ 改造 `.github/workflows/docker-ghcr.yml`：`branches: [production, staging]`，
   tag 用 `github.ref_name` 按分支区分，始终附带 `:sha-<sha>`。
10. ✅ 增加 `deploy` job：向 `codzen-ai/codzen-deployment` 发 `repository_dispatch`
    （`{service: new-api, tag: sha-<sha>}`），再轮询部署 run 状态，失败/超时红灯。
    actionlint 通过。
11. ⬜ 在 new-api 仓库配置 Secret `DEPLOY_PAT`（`repo` + `actions:read`）。
12. ⬜ 删除 new-api 仓库中已不再需要的 `SSH_PRIVATE_KEY` Secret。
13. ⬜ 用一个空提交验证整条链路跑通（构建 → dispatch → rollout → 本仓库变绿）。

> 服务器与部署仓库侧无需改动：`/opt/codzen` 的 compose 已用
> `ghcr.io/codzen-ai/new-api:${NEW_API_TAG:-production}`，`deploy-to-staging.yml`
> 已支持 `repository_dispatch` 并会改写 `.env` 的 `NEW_API_TAG`。

### 阶段 3 · 流程固化

14. 把 `.claude/skills/rebase-production` 拆写为两个 skill：
    - `sync-upstream`：fetch upstream → 更新 `main` → rebase `staging` → force-with-lease push
    - `promote-to-production`：校验已验证 → `git branch -f production <sha>` → 打 tag → push
15. 按 §3.2 的分层顺序整理定制提交（可上游化功能 → fork-only 定制 → CI → 本地资产），
    可在下次 rebase 时顺手完成。
16. 更新 `AGENTS.md` 记录分支约定：`main` 只读镜像、`staging` 唯一定制历史、
    `production` 只移动指针。

### 当前卡点

阶段 2 需要两项信息：staging 域名（健康检查地址）、ECS 上的 compose 目录与部署用户。
阶段 0–1 不依赖这两项，可先行开始。

## 6. 日常操作速查

```bash
# 同步上游
git fetch upstream --prune
git fetch upstream main:main
git rebase main staging
git push --force-with-lease origin staging

# 开发新功能
git switch -c feat/xxx main
# ...开发...
git switch staging && git rebase feat/xxx   # 或 cherry-pick 后重排分层

# 发布到生产
git branch -f production <verified-staging-sha>
git tag prod-$(date +%Y%m%d) production
git push --force-with-lease origin production
git push origin --tags

# 回滚
git branch -f production prod-<上一个日期>
git push --force-with-lease origin production
```
