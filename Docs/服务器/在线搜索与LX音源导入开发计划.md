# 在线搜索与 LX 音源导入开发计划

2026-11-15，用户授权先实现单个 LX JavaScript 音源链接导入，实际可用音源稍后提供。开发在 `main`，本期本地提交，推送及发行等待用户明确要求。

## 功能与边界

导航栏增加“在线搜索”，支持平台搜索、选择音质、下载到服务器及加入本人歌单。音源由管理员通过链接导入、测试、启停和删除。仅针对标准 LX 接口建立兼容层，真实第三方脚本兼容性在用户提供链接后单独验收。

主服务负责权限、外部 HTTP、任务及入库。独立 Node worker 容器负责脚本执行，采用隔离内部网络、只读根、资源限制及受控网络代理，脚本无法挂载业务数据。新功能未配置 worker 时显示明确提示，既有曲库功能继续运行。订阅清单、自动更新、多源优先级留待后续。

## 执行进度

- [ ] O01 固定 worker、搜索和网页 API 契约，记录计划与验收条件。
- [ ] O02 实现 LX 脚本 worker 与受控网络接口，覆盖初始化、URL 获取、超时和异常。
- [ ] O03 实现平台搜索适配及聚合，规范化曲目信息、平台状态及分页。
- [ ] O04 实现管理员音源导入管理、账户下载任务、容量限制、取消及持久化恢复。
- [ ] O05 复用现有校验、去重、配额、提交回执及本人歌单入库，防止重试产生重复状态。
- [ ] O06 实现在线搜索、音源管理和下载任务网页，覆盖手机、账户隔离和异步竞态。
- [ ] O07 补齐 Docker worker 配置、使用说明及 CI 门禁，完成针对性自动化和真实容器验证。
- [ ] O08 使用用户后续提供的真实 LX 音源验证兼容性与可用平台。

## 文件与职责

- `server/internal/online/` 提供安全 HTTP、平台搜索及 worker 通信类型，独立测试。
- `server/online-worker/` 提供 Node 执行器、Dockerfile 和标准 LX API 兼容测试。
- `server/internal/music/online_*.go` 提供业务持久化、管理和用户 API、下载及入库。
- `server/internal/music/online-search.js`、`online-search.css` 及导航集成提供网页。
- `server/compose.yaml` 和 `server/deploy/docker.md` 定义隔离网络、worker 运行及升级说明。

## 接口契约

所有业务接口位于 `/api/v2/online/`，沿用账号会话和 CSRF，播放器 Key 不开放在线下载权限。

- `GET sources` 返回 `{sources,worker_available}`，管理员以外隐藏原始链接与脚本。`POST sources` 接收 `{url}`，抓取脚本并测试初始化。`PATCH sources/{id}` 接收 `{enabled}`，`DELETE` 删除未使用源。
- `GET search?q=&platform=all&page=1&limit=20` 返回 `{items,platforms,has_more}`。每项包含 `platform,id,title,artist,album,duration,cover_url,qualities,music_info`，`music_info` 保留 LX 所需原始字段。平台状态包含 `platform,error,has_more`。仅已安装搜索适配器和已启用源支持的平台参与搜索。
- `GET jobs` 返回 `{jobs}`。`POST jobs` 接收 `{source_id,item,quality,target_playlist_id}`，返回任务。`POST jobs/{id}/cancel` 取消，`POST jobs/{id}/retry` 重试。
- 任务字段为 `id,title,platform,quality,status,downloaded,total,error,track_id,created_at`。状态为 `queued,resolving,downloading,validating,completed,failed,cancelled`，完成入库继续遵循 UUID 规则。
- Worker `POST /execute` 接收 `{script,action,request,broker}`。`action=init` 返回 `{sources}`；`action=request` 通过标准 `lx.EVENT_NAMES.request` 调用并返回 `{result}`。`request` 为 `{source,action,info}`，`broker` 为临时 `{url,token}`。每次执行独立上下文及总超时。
- Worker 的 `lx.request` 通过 broker 执行。broker 接收 `{url,method,headers,body}`，返回 `{statusCode,headers,body}`，统一限制目标公网地址、重定向、时限及响应大小。临时 token 仅允许当前请求周期及受限请求次数。

## 验收条件

先用受控源与 HTTP 夹具复现接口行为。覆盖脚本初始化、音质声明、定时器与网络回调、错误和死循环终止，公网地址校验及重定向，平台部分失败、结果规范化，管理员与成员权限、配额与邮箱校验、取消重试、重启恢复、入库幂等及目标歌单撤权。下载进度在内容长度未知时显示已下载字节。账户切换后丢弃旧响应，普通用户看不到脚本及凭据。

真实 Docker 验证隔离 worker、网页、受控下载和重建数据保留。完整跨平台 CI 在用户授权推送后执行。第三方服务接口变化、地区限制和脚本环境差异按真实结果记录，不能将受控夹具通过等同于全部音源兼容。
