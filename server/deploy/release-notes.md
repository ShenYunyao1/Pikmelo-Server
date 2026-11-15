# Pikmelo Server v0.21.20
<!-- musicserver-release: {"version":"0.21.20","schema":9,"status":"stable"} -->

Pikmelo Server · 拾律服务器，数据库 schema 9，Linux amd64 Docker 发行。新增在线搜索、LX 音源导入和下载入库，主服务与隔离音源 worker 配套发布。

## 本版功能

- 导航栏增加“在线搜索”，支持酷我、酷狗、QQ、网易和咪咕搜索，显示平台状态并分页加载。
- 管理员可导入 LX JavaScript 音源链接，测试初始化并启停或删除。成员使用已启用源，下载时选择音质及本人歌单。
- 下载任务显示进度，支持取消和重试，复用上传校验、去重、账户音频及附件配额、提交回执和歌单入库逻辑。
- 音源脚本在独立非 root、只读根、内部网络和资源限制的 worker 容器执行，公网请求通过带短期授权和地址校验的主服务 broker。
- 任务展示请求音质及实际探测的格式、码率。请求 FLAC 而返回其他格式时显示提示。

## 真实音源测试范围

六音 v1.2.1 的原始链接与加速链接均通过导入，五个平台搜索均返回结果。网易 128k／320k 和 QQ 128k 的指定样本成功下载、校验并加入本人歌单。酷我／酷狗／咪咕的所测样本解析失败，可用性取决于曲目及音源上游。

网易 FLAC 测试样本实际返回 MP3 320 kbps，已验证任务显示格式不符提示。multipart、依赖 DOM 或动态代码生成的脚本，以及加密 QRC／KRC 歌词仍需后续适配。完整样本、脚本摘要和验证记录见 [在线搜索开发计划](https://github.com/ShenYunyao1/Pikmelo-Server/blob/v0.21.20/Docs/服务器/在线搜索与LX音源导入开发计划.md)。

## Docker 镜像与升级

两个镜像须选用同一版本，可使用 Release 清单中的摘要固定身份。

```text
ghcr.io/shenyunyao1/pikmelo-server:0.21.20
ghcr.io/shenyunyao1/pikmelo-server-online-worker:0.21.20
```

`container-release.json` 记录版本、schema、源码提交及两个镜像摘要，各镜像附带来源证明。

已有 Docker 安装先取得本标签下的新版 `server/compose.yaml` 和配置模板，保留本机 `.env`、数据卷名称及自定义路径，在 `.env` 设置两个镜像的目标版本。随后按使用说明停机备份业务卷、拉取并重建两个服务，检查健康后恢复访问。

```sh
docker compose pull pikmelo online-worker
docker compose up -d --no-build --wait
docker compose ps
```

本版保持 schema 9，已有曲库、账户、Key、UUID、歌单及媒体继续使用。音源与任务状态随数据库备份保存，服务重启后中断任务可由用户重试。既有 schema 8 数据由应用按已支持路径备份并事务迁移，跨 schema 回退须恢复对应停机快照。

完整初始化、备份、升级、回退和已有安装导入步骤见 [Docker 使用说明](https://github.com/ShenYunyao1/Pikmelo-Server/blob/v0.21.20/server/deploy/docker.md)。
