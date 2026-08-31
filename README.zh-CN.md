# CodexLink

CodexLink 让 ChatGPT 通过经过 OAuth 保护的只读 MCP 接口查看本地工作区；Codex 继续负责修改文件、执行命令和运行测试。

## 一条命令安装并启动

在需要连接的项目目录执行：

```sh
curl -fsSL https://raw.githubusercontent.com/joeykchen/codexlink/main/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/joeykchen/codexlink/main/install.ps1 | iex
```

安装器会自动完成：

```text
识别系统与 CPU
→ 下载对应的预编译包
→ 校验 SHA-256
→ 原子安装 CodexLink
→ 安装随包提供的 cloudflared
→ 自动写入用户 PATH
→ 缺少 Git 时调用系统安装器自动配置
→ 直接启动当前工作区的一键引导
```

普通用户不需要手工安装 Go、Homebrew、cloudflared、ripgrep，或执行平台包管理器命令。Go 只用于源码开发；全文搜索有纯 Go 回退实现；cloudflared 随每个平台的 CodexLink 发布包一起交付。

安装完成后，任何项目都只需要：

```sh
cd /path/to/project
codexlink
```

## 首次连接 ChatGPT

本机安装与启动已经自动化，但 **ChatGPT 必须由账号所有者对新的 MCP App 完成一次安全确认**。该确认用于核对工作区、只读权限和 OAuth 授权，CodexLink 不会绕过登录、验证码、两步验证或授权确认。

每个工作区首次连接时：

1. 运行 `codexlink`，保留自动打开的本机 Setup 页面。页面会显示：
   - App 名称，例如 `CodexLink · spx`；
   - MCP Endpoint，例如 `https://…/mcp`；
   - 一次性配对码。
2. 使用 **ChatGPT 网页版**，确保当前账号已启用 Developer mode。入口通常位于：
   - `Settings → Apps → Advanced Settings → Developer mode`；或
   - `Workspace settings → Apps → Create`。

   实际入口取决于 ChatGPT 套餐、工作区角色和管理员策略；看不到 Create 时，需要工作区管理员授予权限。
3. 创建一个自定义 App：
   - Name：复制 Setup 页面中的 App 名称；
   - MCP Endpoint：复制 Setup 页面中的地址；
   - Authentication：选择 `OAuth`。
4. 点击 `Scan Tools`。
5. ChatGPT 打开 CodexLink 授权页后：
   - 确认工作区名称正确；
   - 确认请求的是只读权限；
   - 输入 Setup 页面中的一次性配对码；
   - 点击连接或授权。
6. 回到 ChatGPT，等待工具扫描完成。确认出现八个只读工具后，点击 `Create`。
7. 新建对话，选择刚创建的 CodexLink App，并发送：

```text
调用 workspace_info，确认当前连接的工作区。
```

正常情况下，同一个工作区只确认一次。以下情况需要重新确认：

- 执行了 `codexlink unpair`；
- OAuth Token 被撤销或过期且无法刷新；
- 删除或重建了 ChatGPT App；
- 临时 Tunnel 地址发生变化，Setup 页面提示需要更新 App。

ChatGPT 自定义 MCP App 的入口和权限可能随产品版本变化，最新流程以 [OpenAI 的 Developer mode 与 MCP Apps 说明](https://help.openai.com/en/articles/12584461-developer-mode-and-full-mcp-connectors-in-chatgpt) 为准。

## 最小心智模型

```text
ChatGPT -- OAuth + MCP --> CodexLink -- 只读 --> 工作区
Codex   --------------- 修改/命令/测试 -----> 工作区
```

一个工作区目录就是一个授权边界。ChatGPT 可以调用八个只读工具：

```text
workspace_info
list_directory
read_file
search_workspace
git_status
git_diff
test_status
execution_summary
```

公网 MCP 不提供写文件、删除文件、Shell、安装依赖、Git 提交或 Push 能力。

## 常用命令

```sh
codexlink          # 幂等地安装配置、启动或复用服务
codexlink status   # 状态
codexlink doctor   # 检查和自动修复
codexlink pair     # 生成新的配对码
codexlink unpair   # 撤销当前工作区的授权
codexlink stop     # 停止当前工作区服务
```

## 两轮重构

### 第一轮：统一可移植发布模型

平台名称、可执行文件名称、Cloudflare 资产名称、归档格式、归档安全检查、确定性打包和 SHA-256 生成全部集中到 `internal/distribution`。发布流程和安装器不再各自维护平台判断逻辑。

### 第二轮：自包含、一步式安装

部署单元从“源码 + 用户手工安装平台依赖”改为“CodexLink + cloudflared 的平台发布包”。Unix 与 Windows 安装器共享同一资产命名约定，执行校验、原子替换、PATH 配置和依赖修复，并在安装后直接进入工作区引导。

详细设计见 [`docs/installation.md`](docs/installation.md) 和 [`docs/engineering/refactor-log.md`](docs/engineering/refactor-log.md)。

## 源码开发

普通用户不需要 Go。贡献者可以执行：

```sh
make check
make build
make install-dev
```

CodexLink 是独立社区项目，不是 OpenAI 官方产品。
