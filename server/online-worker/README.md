# LX 音源执行器

`POST /execute` 接收音源脚本、`init`／`request` 操作及临时 broker 地址和令牌。每次请求创建独立 worker thread，执行最长 15 秒，最多同时运行 4 个任务。初始化返回音源能力，请求返回脚本结果。运行时仅注入标准 LX 兼容接口，HTTP 请求通过主服务 broker 验证目标和预算。

```sh
node --test runner.test.mjs
node server.mjs
```

执行器在隔离内部 Docker 网络运行，使用非 root 用户、只读根文件系统和资源限制，独立于业务数据卷。主服务只向执行器提供本次操作所需的脚本、曲目信息和短期网络能力令牌。`node:vm` 用于组织接口作用域，容器、挂载与网络规则共同提供隔离边界。执行器端口仅供内部服务访问。

当前兼容 `lx.on`、`lx.send`、`lx.request`、定时器、Buffer 转换、MD5／AES／RSA／随机字节及 zlib。脚本元数据识别 `/**`、`/*!` 和普通块注释头。HTTP 支持正文和表单，multipart 暂不支持。`updateAlert` 接收后结束，不自动获取或执行更新脚本。具体第三方音源需结合用户提供的脚本验证。

HTTP 回调按照 LX 桌面版行为尝试解析全部 JSON 文本，保留非 JSON 字符串，响应头键转为小写，并提供 `raw` 和 `bytes`。文本类型响应中返回的 JSON 也可作为对象使用。
