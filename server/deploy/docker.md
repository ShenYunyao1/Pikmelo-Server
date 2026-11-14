# Pikmelo Server Docker 使用与升级

适用 `main` 的 Docker 运行方式、Linux amd64 容器、Docker Engine 与 Compose v2。2026-11-15 起 Docker 实现合并至本地主分支，原分支保留历史。运行实例通过 Docker 命令管理升级，网页“服务器更新”显示容器托管说明。

## 首次启动

将 `server/compose.yaml` 和 `server/docker.env.example` 放入部署目录，按模板保存 `.env`。正式镜像地址为 `ghcr.io/shenyunyao1/pikmelo-server`，首个正式版本 [v0.21.19](https://github.com/ShenYunyao1/Pikmelo-Server/releases/tag/v0.21.19) 已通过 Actions、匿名拉取及来源证明验收，支持 Linux amd64、schema 9。

在 `.env` 设置 `PIKMELO_IMAGE=ghcr.io/shenyunyao1/pikmelo-server:0.21.19` 可固定该版本，默认 `latest` 跟随 Docker 稳定通道。需要固定不可变镜像时，填写以下完整引用。

```text
ghcr.io/shenyunyao1/pikmelo-server@sha256:4a3807a4c64162c0774619c7ce3feb2a360f57129a2a0911d15953f409b83eb8
```

在 Compose 文件所在目录执行以下命令。

```sh
docker compose pull pikmelo
docker compose pull online-worker
docker compose up -d --no-build --wait --wait-timeout 120 pikmelo
docker compose logs --tail 50 pikmelo
docker compose exec -T pikmelo cat /data/data/setup-token
```

访问 `http://127.0.0.1:8080`，使用最后一条命令读取的授权码初始化管理员。密码在网页设置，授权码仅供本次初始化使用。

源码本地构建可在 `server/` 目录执行以下命令。

```sh
docker compose build pikmelo
docker compose build online-worker
docker compose up -d --no-build --wait --wait-timeout 120 pikmelo
```

本地源码构建默认标为未知来源及修改状态。发行构建由 CI 写入真实源码提交、版本及干净状态。源码构建镜像与远程发行采用相同配置项。

## 配置与数据

| 配置项 | 默认值 | 用途 |
| --- | --- | --- |
| PIKMELO_IMAGE | ghcr.io/shenyunyao1/pikmelo-server:latest | 稳定通道，亦可填写具体版本或 `@sha256` 摘要 |
| PIKMELO_WORKER_IMAGE | ghcr.io/shenyunyao1/pikmelo-server-online-worker:latest | 配套音源 worker，与主镜像选择同一发行版本 |
| PIKMELO_DATA_VOLUME | pikmelo-data | 业务数据卷，每个实例使用独立名称 |
| PIKMELO_BIND_ADDRESS | 127.0.0.1 | 宿主机监听地址 |
| PIKMELO_PORT | 8080 | 宿主机端口，容器内部固定 8080 |
| PIKMELO_PUBLIC_URL | 空 | HTTPS 反向代理后的公开 origin |
| PIKMELO_DATABASE | /data/data/library.sqlite | 容器内数据库位置 |
| PIKMELO_MEDIA_ROOT | /data/media | 容器内媒体根目录 |

业务卷挂载到 `/data`，保存数据库及 WAL、媒体和歌词、封面、续传任务、邮件凭据、系统活动日志、备份及应用设置。`pikmelo-data-tmp` 挂载到 `/tmp`，供 multipart 等临时文件使用。两者使用磁盘容量，上传继续执行服务端的空间预留与限额校验。

应用以 UID/GID `10001:10001` 运行，根文件系统只读。保留 Compose 定义的数据和临时卷挂载，实例使用单个数据库写进程。停止、重建和升级继续使用同一业务卷。

需要局域网访问时，按环境配置 `PIKMELO_BIND_ADDRESS`。HTTPS 反向代理接入时填写 `PIKMELO_PUBLIC_URL` 并沿用对应认证及来源校验配置。

## 通过 Docker 命令受控升级

新容器启动后由服务端执行已支持的数据库迁移，当前已验证 schema 8→9 的账户、Key、歌单及媒体保留。后续 schema 变化继续以发行说明与迁移测试声明的支持范围为准。升级前先停止旧实例并备份持久卷，跨 schema 回退时使用原镜像与升级前快照。

以下示例采用默认卷名。自定义卷名时相应替换命令参数，`OLD_IMAGE_ID` 表示第一条命令查到的原镜像 ID。

升级窗口先暂停外部写入。完成健康及业务验收后，再开放访问。

### 1. 记录原镜像并停机备份

```sh
docker compose images -q pikmelo
docker image tag OLD_IMAGE_ID pikmelo-server:rollback-local
docker compose stop pikmelo
docker run --rm --network none --user 0 --mount type=volume,src=pikmelo-data,dst=/source,readonly --mount type=volume,src=pikmelo-backups,dst=/backup --entrypoint tar pikmelo-server:rollback-local -C /source -cpf /backup/pre-upgrade.tar .
docker run --rm --network none --mount type=volume,src=pikmelo-backups,dst=/backup,readonly --entrypoint tar pikmelo-server:rollback-local -tf /backup/pre-upgrade.tar
```

停机确认退出成功后执行快照，包含原库、媒体、邮件凭据和上传检查点。保留原镜像及快照到本次升级验收完成。多个升级窗口使用不同快照名称归档。

### 2. 拉取并重建

将 `.env` 中的 `PIKMELO_IMAGE` 和 `PIKMELO_WORKER_IMAGE` 选择为同一目标发行的版本或对应镜像摘要。使用 `latest` 时保持稳定通道配置。

```sh
docker compose pull pikmelo
docker compose pull online-worker
docker compose up -d --no-build --wait --wait-timeout 120 pikmelo
docker compose ps
docker compose logs --tail 100 pikmelo
docker compose exec -T pikmelo pikmelo-server healthcheck
```

`up --wait` 等待健康检查通过。镜像更新和服务健康情况由操作人确认，验收包括登录、曲库、歌单、上传及媒体读取。数据库按已有 schema 8→9 事务迁移策略升级。

### 3. 失败回退

健康失败时先保持升级窗口，停止失败容器。采用“原镜像＋原快照”的完整回退，避免跨 schema 启动旧程序或保留半次升级写入。

```sh
docker compose stop pikmelo
docker volume create pikmelo-rollback-data
docker run --rm --network none --user 0 --mount type=volume,src=pikmelo-rollback-data,dst=/restore --mount type=volume,src=pikmelo-backups,dst=/backup,readonly --entrypoint tar pikmelo-server:rollback-local -C /restore -xpf /backup/pre-upgrade.tar
```

`pikmelo-rollback-data` 应为本次回退新建的空卷。随后将 `.env` 的 `PIKMELO_IMAGE` 设为 `pikmelo-server:rollback-local`、`PIKMELO_DATA_VOLUME` 设为 `pikmelo-rollback-data`，并保持原数据库与媒体路径配置，执行以下命令。

```sh
docker compose up -d --no-build --wait --wait-timeout 120 pikmelo
docker compose exec -T pikmelo pikmelo-server healthcheck
```

回退恢复到停机快照对应的业务状态。保留失败卷供排查，先验证恢复后的账户、歌单、Key 和媒体，再开放写入。

## 从已有安装导入

先由已有实例生成包含媒体的完整应用备份，并按现有恢复流程验收归档。使用当前容器镜像的 `backup restore` 命令恢复到新卷的独立子目录。例如完整备份在宿主机的 `BACKUP_DIRECTORY` 中。

```sh
docker run --rm --network none --mount type=volume,src=pikmelo-import-data,dst=/data --mount type=bind,src=BACKUP_DIRECTORY,dst=/backup,readonly ghcr.io/shenyunyao1/pikmelo-server:latest backup restore --file /backup/library.tar.gz --destination /data/imported
```

使用备份所在目录的绝对路径替换 `BACKUP_DIRECTORY`。导入完成后设置 `PIKMELO_DATA_VOLUME=pikmelo-import-data`、`PIKMELO_DATABASE=/data/imported/data/library.sqlite`、`PIKMELO_MEDIA_ROOT=/data/imported`，再通过 Compose 启动。

完整应用备份恢复会按既有规则清理旧会话、关闭自动备份并重置邮件服务状态，恢复后重新核对这些设置。在网页服务器设置中将音频信息工具路径留空或设置为容器内 `ffprobe`，曲目继续按相对媒体路径读取。已有部署保留到导入与业务验收完成。

## 构建与发行验证

### 在线搜索与音源 worker

当前开发版增加 `online-worker` 服务。该 worker 镜像随下一次正式发行发布，v0.21.19 的历史发行保持原样。本地源码验证先执行 `docker compose build pikmelo online-worker`，再启动 Compose。正式更新从对应源码取得新版 Compose 及配置模板，配套拉取两个镜像。

worker 仅连接 `internal` Docker 网络，不发布宿主端口、不挂载数据库或媒体。主服务连接该内部网络及普通出站网络，脚本通过短期授权 broker 访问公网 HTTP(S)，逐次检查 DNS、重定向及流量预算。主服务继续负责下载、配额、校验及入库。

网页在“在线搜索”提供搜索、下载任务及管理员音源管理。管理员导入一个指向 UTF-8 `.js` 的链接，最大 512 KiB，通过初始化测试后启用。最多保存八个音源，普通用户可使用已启用音源，源地址和脚本对普通用户隐藏。此期脚本保存在 SQLite 的独立 `meta` 命名空间中，随数据库备份保留，schema 保持 9，现有曲库数据无需重导。

下载沿用账户上传权限、邮箱策略和音频／附件额度，同时受临时预算保护。无 Content-Length 的响应先预留不超过 64 MiB 的上限并显示已下载字节，结束后按实际大小校验。重启后未完成任务标记为可重试，保留有效续传检查点；用户点击重试后重新验证会话和权限，强 ETag 与 Content-Range 一致时继续，否则重新下载。取消、损坏及过期预览重试会重建检查点。

兼容范围为标准 LX `musicUrl`、定时器、网络回调及有限的 Buffer／加密／压缩工具。multipart 和依赖浏览器 DOM、动态代码生成等特殊环境的脚本需要后续适配。搜索适配 `kw/kg/tx/wy/mg`，当前普通歌词支持酷我／网易、具有 LRC 的咪咕／酷狗；加密 QRC／KRC 等暂不解码。可选附件失败记录日志，音频可独立入库。真实音源兼容性和平台服务可用性另行验证。

原生直接启动时可通过 `PIKMELO_ONLINE_WORKER_URL` 指向隔离 worker，并设置 `PIKMELO_ONLINE_BROKER_URL` 为该 worker 能访问的主服务 broker 地址。未配置 worker 时页面提示不可用，既有曲库功能继续运行。Compose 已配置这两个地址，修改网络或服务名时需保持可达。

专项验证命令如下。

```sh
node --test online-worker/runner.test.mjs
go test ./internal/online -count=1
MUSIC_SERVER_LIVE_ONLINE_WORKER=1 go test ./internal/music -run '^TestOnline' -count=1
node tests/online-search-ui.cjs
```

浏览器专项沿用测试目录的 Playwright。平台适配来源及许可位于 `internal/online/NOTICE` 和 `LICENSE-LX-Music.txt`，容器同时包含这两份文件。

本分支的服务端 CI 验证 Go／vet、网页、镜像及实际容器升级回退，版本标签与源码版本一致时发布 GHCR 版本镜像、稳定通道和 GitHub Release。Release 的 `container-release.json` 记录版本、schema、源码提交及镜像摘要，流水线同时生成镜像来源证明。

Docker 镜像的 `latest` 为独立稳定通道。Docker 专用 GitHub Release 使用 `--latest=false`，原生安装的更新器通过原生稳定 Release 获取其签名平台资产。仓库版本标签是全局唯一的，后续发行使用新的源码版本和标签。

本地运行容器回归可执行以下命令，测试使用独立项目和随机卷名，完成后清理本次创建的容器与卷。

```sh
docker build -t pikmelo-server:test .
docker build -t pikmelo-online-worker:test online-worker
GOOS=linux CGO_ENABLED=0 python tests/build-schema-preview.py --version 0.21.18
python tests/docker-smoke.py --image pikmelo-server:test --worker-image pikmelo-online-worker:test --schema8-binary build/schema-preview/musicserver --browser-test
```

浏览器回归使用 `tests/package-lock.json` 中的 Playwright。Docker Desktop 的 CLI 目录需要在当前进程 PATH 中，构建遇到失效加速器时，可通过 `GO_IMAGE` 和 `RUNTIME_IMAGE` 构建参数选择对应的官方 Registry 地址，并按环境为当前构建进程配置代理。

```sh
docker compose build --build-arg GO_IMAGE=registry-1.docker.io/library/golang:1.26-bookworm --build-arg RUNTIME_IMAGE=registry-1.docker.io/library/debian:bookworm-slim pikmelo
```

相关机制见 [多阶段构建](https://docs.docker.com/build/building/multi-stage/)、[Compose 拉取](https://docs.docker.com/reference/cli/docker/compose/pull/) 和 [Compose 重建与健康等待](https://docs.docker.com/reference/cli/docker/compose/up/)。
