# Pikmelo Server v0.21.21
<!-- musicserver-release: {"version":"0.21.21","schema":9,"status":"stable"} -->

Pikmelo Server · 拾律服务器，数据库 schema 9，Linux amd64 Docker 发行。扩展在线搜索候选音质展示、完善 LX 音源兼容，并更新网页 Logo 与播放封面布局。主服务与隔离音源 worker 配套发布。

## 本版功能

- 搜索列表显示前四项候选音质，更多候选在曲目详情中查看。格式、编码、码率、大小、采样率、位深、声道及平台格式码按有效字段展示。
- 点击可用音质后，在导入弹窗预选支持它的音源和对应档位。切换音源时同步更新可选档位和属性，当前音源未支持的候选以只读方式展示。
- 候选信息统一标明“平台标称”，导入后由音频探测给出实际参数。酷我大小保留接口原文，无损档位码仅用于识别档位。
- LX 音源初始化按执行器支持能力协商，补齐标准 Buffer 二进制接口，音源脚本大小上限提高到 1 MiB。镜像包含相关上游许可与出处说明。
- 接入服务器 Logo 与网页图标，修复在线搜索样式对在线播放横幅封面尺寸的影响，桌面和手机封面保持正方形。
- 改进浏览器测试端口分配，遇到 Fetch 禁用端口时自动重新分配。

## 真实音源测试范围

对 pdone/lx-music-source 固定快照的 20 份脚本进行初始化验证，其中 18 份通过。`lx` 在 15 秒内未完成初始化，`suyin` 依赖当前未支持的配置界面事件。初始化成功后，具体曲目仍受音源上游可用性影响。

fish_music 1.9.0 的酷我／酷狗／QQ／网易／咪咕指定样本完成下载、校验及加入本人歌单，网易 FLAC 样本实际探测为 FLAC。QQ 128k 样本实际返回 M4A 约 98 kbps，候选参数与实际下载参数分别记录。其他音源的部分样本仍解析失败。

五平台候选元数据已抽样核对，重复查询时 QQ 搜索曾暂时不可用。multipart、依赖 DOM 或动态代码生成的脚本，以及加密 QRC／KRC 歌词仍需后续适配。完整样本、脚本摘要和验证记录见 [在线搜索开发计划](https://github.com/ShenYunyao1/Pikmelo-Server/blob/v0.21.21/Docs/服务器/在线搜索与LX音源导入开发计划.md)。

## Docker 镜像与升级

两个镜像须选用同一版本，可使用 Release 清单中的摘要固定身份。

```text
ghcr.io/shenyunyao1/pikmelo-server:0.21.21
ghcr.io/shenyunyao1/pikmelo-server-online-worker:0.21.21
```

`container-release.json` 记录版本、schema、源码提交及两个镜像摘要，各镜像附带来源证明。

已有 Docker 安装先取得本标签下的新版 `server/compose.yaml` 和配置模板，保留本机 `.env`、数据卷名称及自定义路径，在 `.env` 设置两个镜像的目标版本。随后按使用说明停机备份业务卷、拉取并重建两个服务，检查健康后恢复访问。

```sh
docker compose pull pikmelo online-worker
docker compose up -d --no-build --wait
docker compose ps
```

本版保持 schema 9，已有曲库、账户、Key、UUID、歌单及媒体继续使用。音源与任务状态随数据库备份保存，服务重启后中断任务可由用户重试。既有 schema 8 数据由应用按已支持路径备份并事务迁移，跨 schema 回退须恢复对应停机快照。

完整初始化、备份、升级、回退和已有安装导入步骤见 [Docker 使用说明](https://github.com/ShenYunyao1/Pikmelo-Server/blob/v0.21.21/server/deploy/docker.md)。
