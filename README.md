# TVBox Source Merger

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Docker](https://img.shields.io/docker/pulls/nevermindzzt/tvbox-merger?label=Docker%20Pulls)](https://hub.docker.com/r/nevermindzzt/tvbox-merger)
[![Build](https://github.com/nevermindzzt/tvbox-merger/actions/workflows/docker-publish.yml/badge.svg)](https://github.com/nevermindzzt/tvbox-merger/actions/workflows/docker-publish.yml)

将多个 TVBox JSON 源合并为一个聚合源 — 支持自动刷新、智能缓存和美观的管理后台。

## 功能特点

- **智能合并** — 合并多个 TVBox 源，sites 按 `key` 去重、parses 按 `name+url` 去重、lives 按 `name` 去重
- **多格式支持** — 支持纯 JSON、JPEG 嵌入 base64 JSON（如饭太硬）、含 JS 注释的 JSON
- **优雅降级** — 源离线时自动使用缓存版本，仅当缓存也失效时才从合并源中剔除
- **自动刷新** — 可配置的 cron 定时刷新（默认每 6 小时），管理面板也支持手动触发
- **管理后台** — 毛玻璃风格 UI，支持源管理、健康状态查看、一键刷新
- **JWT 认证** — 安全的登录/登出机制
- **Docker 部署** — 单容器部署，通过 docker-compose 一键启动

---

## 开发指南

### 前置条件

- Go 1.22 或更高版本
- Git

### 本地运行

```bash
# 1. 进入项目目录
cd tvbox-merger  # 或项目所在目录

# 2. 创建数据目录
mkdir -p data/cache

# 3. （可选）配置环境变量
# 复制 .env.example 为 .env 并修改其中的密码和 JWT 密钥
cp .env.example .env

# 4. 下载依赖
go mod tidy

# 5. 启动服务（热加载模式）
go run .
```

启动后终端会输出 `TVBox Merger started on port 8080`，打开浏览器访问：
- 管理后台：`http://localhost:8080/admin/login`（默认账号 `admin` / `admin123`）
- 合并源地址：`http://localhost:8080/tvbox.json`

### 开发中常用命令

```bash
# 编译（检查语法错误）
go build ./...

# 静态分析
go vet ./...

# 运行测试（如果有）
go test ./...

# 编译为可执行文件
go build -o tvbox-merger .
```

### 目录结构说明

```
.
├── main.go                 # 入口文件
├── internal/               # Go 源码
│   ├── auth/               # JWT 认证模块
│   ├── config/             # 环境变量配置
│   ├── database/           # SQLite 数据库操作
│   ├── fetcher/            # HTTP 抓取 + 源格式检测
│   ├── handler/            # API 路由 + 模板渲染
│   ├── merger/             # TVBox 源合并去重逻辑
│   ├── model/              # 数据模型
│   └── scheduler/          # cron 定时刷新器
├── web/templates/          # HTML 模板（Tailwind CSS）
├── data/                   # 运行时数据目录（自动创建）
│   ├── tvbox.db            # SQLite 数据库
│   └── cache/              # 源缓存文件
├── Dockerfile              # Docker 多阶段构建
├── docker-compose.yml      # Docker Compose 配置
└── .env                    # 环境变量配置（从 .env.example 复制）
```

---

## 部署指南

### 方式一：从 Docker Hub 拉取（推荐）

项目已配置 GitHub Actions 自动构建，每次推送 `main` 分支或打 `v*` 标签时自动构建多架构镜像（linux/amd64, linux/arm64）。镜像托管于 [nevermindzzt/tvbox-merger](https://hub.docker.com/r/nevermindzzt/tvbox-merger)。

```bash
# 使用 docker-compose.pull.yml 一键拉取并运行
docker compose -f docker-compose.pull.yml up -d
```

```bash
# 拉取最新镜像
docker pull nevermindzzt/tvbox-merger:latest

# 运行容器
docker run -d \
  --name tvbox-merger \
  -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD=your-password \
  -e JWT_SECRET=your-random-secret \
  nevermindzzt/tvbox-merger:latest
```

### 方式二：Docker Compose 本地构建

```bash
# 1. 进入项目目录
cd tvbox-merger

# 2. 复制并编辑环境配置
cp .env.example .env
# 编辑 .env，设置 ADMIN_PASSWORD 和 JWT_SECRET

# 3. 启动服务
docker compose up -d

# 查看日志
docker compose logs -f
```

服务启动后访问 `http://localhost:8080`。

### 方式三：Docker 本地构建

```bash
docker build -t tvbox-merger .
docker run -d \
  --name tvbox-merger \
  -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD=your-password \
  -e JWT_SECRET=your-random-secret \
  tvbox-merger
```

### 方式四：直接编译运行

```bash
go build -o tvbox-merger .
./tvbox-merger
```

---

## 环境变量说明

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8080` | 服务监听端口 |
| `ADMIN_USERNAME` | `admin` | 管理员登录用户名 |
| `ADMIN_PASSWORD` | `admin123` | 管理员登录密码 |
| `JWT_SECRET` | `tvbox-merger-secret-key-change-me` | JWT 签名密钥（生产环境务必修改！） |
| `REFRESH_INTERVAL` | `6h` | 源刷新间隔（如 `30m`、`2h`、`12h`） |
| `CACHE_MAX_AGE` | `24h` | 缓存最大有效期 |
| `DATA_SOURCE` | `data/tvbox.db` | SQLite 数据库路径 |
| `CACHE_DIR` | `data/cache` | 缓存目录 |
| `DEFAULT_UA` | *(手机 Chrome UA)* | HTTP 请求的 User-Agent |
| `TZ` | `Asia/Shanghai` | 容器时区 |

---

## 使用教程

1. **打开管理后台** `http://localhost:8080/admin/login`
2. **添加 TVBox 源** — 点击「Add Source」或使用「Quick Add」快捷添加已知源
3. **等待自动刷新** 或点击「Refresh Now」立即触发合并
4. **在 TVBox 中配置** 以下地址作为订阅源：
   ```
   http://你的服务器地址:8080/tvbox.json
   ```

### 快捷添加的已知源

| 名称 | URL | 格式 |
|------|-----|------|
| 饭太硬 | `http://www.饭太硬.cc/tv` | JPEG 嵌入 base64 JSON |
| wex | `https://9280.kstore.space/wex.json` | 纯 JSON |
| nxog（装歌） | `https://tv.nxog.top/m/` | 纯 JSON |

---

## API 接口

### 公开接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/` | 首页 |
| GET | `/tvbox.json` | **合并后的 TVBox 源**（在 TVBox 中配置此地址） |

### 管理接口（需认证）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/login` | 登录页面 |
| POST | `/admin/login` | 登录（JSON: `{"username":"...", "password":"..."}`） |
| POST | `/admin/logout` | 登出 |
| GET | `/admin/` | 管理后台仪表盘 |
| POST | `/admin/sources` | 新增源 |
| PUT | `/admin/sources/:id` | 编辑源 |
| DELETE | `/admin/sources/:id` | 删除源 |
| POST | `/admin/refresh` | 手动触发合并 |
| GET | `/admin/status` | 查看各源健康状态（JSON） |

---

## 工作原理

```
TVBox App  ←GET /tvbox.json──  Go Server  ──HTTP 抓取──→  源1 (JSON)
                                        │                  源2 (JPEG+base64)
                                        │                  源3 (JSON)
                                    ┌───┴───┐
                                  SQLite    缓存文件
                                (源配置)   (原始内容)
                                    │
                               定时合并任务
                           (去重 → 合并 → 存储)
                                    │
                              合并结果 (MergedResult)
```

### 缓存策略

1. 定时抓取每个源
2. 抓取成功 → 原始内容 + 提取的 JSON 存入 SQLite 缓存（标记 `is_valid=true`）
3. 源不可达 → 检查缓存：如果缓存的 JSON 能正常解析，使用缓存版本
4. 缓存也失效 → 从合并源中剔除，直到下次成功抓取

---

## 许可证

Apache 2.0。详见 [LICENSE](LICENSE)。

---

## GitHub Actions 自动构建

项目包含 `.github/workflows/docker-publish.yml`，在以下情况自动构建 Docker 镜像：

| 触发事件 | 推送的标签 |
|---------|-----------|
| 推送 `main` 分支 | `latest` |
| 推送 `v*` 标签（如 `v1.0.0`） | `v1.0.0`、`v1.0`、`latest` |

### 配置步骤

1. 在 [Docker Hub](https://hub.docker.com/) 注册账号
2. 在 GitHub 仓库 Settings → Secrets and variables → Actions 添加：
   - `DOCKER_USERNAME` — 你的 Docker Hub 用户名
   - `DOCKER_PASSWORD` — Docker Hub 密码或[访问令牌](https://hub.docker.com/settings/security)
3. 推送代码到 `main` 分支，Actions 自动构建

### 镜像地址

```
docker pull 你的Docker用户名/tvbox-merger:latest
```
