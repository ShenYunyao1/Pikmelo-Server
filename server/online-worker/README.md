# LX 音源执行器

`POST /execute` 接收音源脚本、`init`／`request` 操作及临时 broker 地址和令牌。每次请求创建独立 worker thread，执行最长 15 秒，最多同时运行 4 个任务。初始化返回音源能力，请求返回脚本结果。运行时仅注入标准 LX 兼容接口，HTTP 请求通过主服务 broker 验证目标和预算。

```sh
node --test runner.test.mjs
node server.mjs
```

执行器在隔离内部 Docker 网络运行，使用非 root 用户、只读根文件系统和资源限制，独立于业务数据卷。主服务只向执行器提供本次操作所需的脚本、曲目信息和短期网络能力令牌。`node:vm` 用于组织接口作用域，容器、挂载与网络规则共同提供隔离边界。执行器端口仅供内部服务访问。

当前兼容 `lx.on`、`lx.send`、`lx.request`、定时器、Buffer 转换、MD5／AES／RSA／随机字节及 zlib。脚本元数据识别 `/**`、`/*!` 和普通块注释头。HTTP 支持正文和表单，multipart 暂不支持。`updateAlert` 接收后结束，不自动获取或执行更新脚本。具体第三方音源需结合用户提供的脚本验证。

HTTP 回调按照 LX 桌面版行为尝试解析全部 JSON 文本，保留非 JSON 字符串，响应头键转为小写，并提供 `raw` 和 `bytes`。文本类型响应中返回的 JSON 也可作为对象使用。

接口兼容参考 LX Music Desktop 固定提交 `323e4bac505da428adae8db6b58039fb2b36b86d` 的 `src/main/modules/userApi/renderer/preload.js`。沿用其能力交集规则，提取宿主支持的平台、动作和音质，脚本额外声明的能力随协商结果过滤。请求阶段继续检查已协商的动作与音质。Buffer 工具支持二进制字符串解码和 ArrayBuffer 的 offset／length 参数，同时受执行器大小限制。来源说明及 Apache-2.0 许可证随 worker 镜像分发。

2026-11-15 的仓库音源矩阵在受限 Compose 环境运行，详细结果见 [在线搜索开发计划](../../Docs/服务器/在线搜索与LX音源导入开发计划.md#多音源兼容性完善)。音源可导入后，还需结合真实曲目验证其上游返回和入库结果。
