# AI 工作平台核心

> 一个用 JSON-RPC over stdio 加载子进程插件的极简微内核。核心只做加载、生命周期、格式化兜底。其余全部是插件。所有变更重启生效。公开契约只增不减。

核心**不认识** AI、不认识 LLM、不认识工具、不认识 UI。
它只认识两件事：**JSON-RPC 2.0** 和 **插件目录里的 `manifest.json`**。

---

## 快速开始

### Windows

```powershell
powershell -ExecutionPolicy Bypass -File build.ps1
```

构建完**项目根目录**会多出一个带图标的 `启动AI工作平台.exe`，**双击它就行**。
它一共一百五十来行：把控制台窗口标题改成产品名、拉起 `runtime\core.exe`、
把退出码原样带回来；发现已经开着一个实例时就直接说明白，不再开第二个去抢终端。

想要桌面上也有一个：

```powershell
powershell -ExecutionPolicy Bypass -File tools\install-shortcut.ps1
```

核心本身还是可以直接跑（脚本、自动化场景用这个）：

```powershell
.\runtime\core.exe
```

### Linux / macOS

```sh
make build
./启动AI工作平台      # 或者 ./runtime/core
make test      # 等价于 go test ./... && go vet ./...
make clean     # 删掉整个 runtime/，源码树不受影响
```

构建完成后：

```
启动AI工作平台[.exe]     # 启动器（入口，双击这个）
runtime/
├── core[.exe]          # 核心二进制
├── app.ico             # 图标（由 launcher/assets/appicon.png 生成）
├── core.json           # 核心配置（格式化不删除）
├── builtin/            # 内置官方插件包
├── plugins/            # 已安装的插件（*.plugin/，构建脚本决定装哪些）
├── plugin-configs/     # 插件配置
├── logs/               # 日志
└── pending-delete/     # 待删除插件（重启后清理）
```

图标和版本信息是**构建时打进 exe 的**：Go 的链接器不写 PE 的 `.rsrc` 段，编译出来的
exe 在资源管理器里是白板图标、右键属性全是空的。`tools/set-windows-resources.ps1`
用 Windows 自带的 `UpdateResource` 补上 RT_ICON / RT_GROUP_ICON / RT_VERSION，
不引任何第三方工具。

启动后由**工作台**（`workbench`）接管当前终端。**不启动任何 HTTP 服务，也不需要浏览器。**

三个页面，**全部可点**：

| 页面 | 内容 | 能点什么 |
|---|---|---|
| 聊天 | 对话区 + 服务链路侧栏 | 点侧栏服务 → 跳到它的插件详情；滚轮翻历史 |
| 插件 | 已安装插件清单（含运行状态） | 点任意插件 → 右侧显示它的服务 / 权限 / 依赖 / 兼容范围，并可「启用/禁用」「删除」 |
| 工作区 | 指定工作区文件夹 | 这一页由 workspace 插件自带；路径可直接填，也可以在目录里点着挑，点「设为工作区」生效 |
| 设置 | AI 接入（云端 / 本地） | ① 选「云端」或「本地」；② 云端＝知名平台快捷连接 + 自定义 / 冷门平台连接，本地＝**系统扫描本机**（看这台机器上有没有 AI）+ 已知本地应用 + 自定义本地服务（扫不到就手填）；③ 右栏选**协议**（OpenAI 兼容 / Ollama 原生 / 离线 mock）、填密钥与模型 → 保存 |

插件页。清单是经能力代理读 `plugins/*/manifest.json` 得到的，所以**新装的插件会自动出现**；
`✗` 表示它声明的服务当前没跑起来（这里 Python 未安装，所以 echo-python 是 `✗`）：

```
┌─ AI 工作平台 · 工作台 ──────────────────────────────── core v1.0.0 · com.official.workbench ─┐
│ 聊天   [插件]   设置                                                               插件 9/10 │
│ 重新扫描 已选中 com.official.echo · 运行中                │ 插件详情                         │
│ ✓ com.official.agent                         v1.0.0       │ com.official.echo                │
│ ✓ com.official.echo                          v1.0.0       │ Echo Tool                        │
│ ✓ com.official.echo-c                        v1.0.0       │ 版本   v1.0.0                    │
│ ✓ com.official.echo-java                     v1.0.0       │ 运行时 binary                    │
│ ✗ com.official.echo-python                   v1.0.0       │ 入口   main                      │
│ ✓ com.official.fs-tool                       v1.0.0       │                                  │
│ ✓ com.official.llm                           v1.0.0       │ 官方示例插件：提供一个回显服务 e │
│ ✓ com.official.session                       v1.0.0       │ cho.v1                           │
│ ✓ com.official.tool-executor                 v1.0.0       │                                  │
│ ✓ com.official.workbench                     v1.0.0       │ 提供的服务                       │
│                                                           │ ✓ echo.v1                        │
│                                                           │ 状态   运行中                    │
│                                                           │                                  │
│                                                           │ 兼容核心 >=1.0 <2.0              │
├───────────────────────────────────────────────────────────┴──────────────────────────────────┤
│ 已选中 com.official.echo · 运行中                                                            │
└─ 单击插件查看详情 · 滚轮滚动 · Tab 切页 · Ctrl+C 复制 · Ctrl+Q 退出 ───────────────────────┘
```

设置页（AI 接入）分两条分支 —— **云端**和**本地** —— 每一栏都只要两三步就能连通：

| 分支 | 有什么 | 走完要做的 |
|---|---|---|
| **云端** | ② 知名平台快捷连接（DeepSeek / Kimi / 智谱 GLM / 通义千问 / 硅基流动 / OpenAI / 百度文心 / 腾讯混元 / 字节豆包 / 讯飞星火 / MiniMax / 阶跃星辰 / 零一万物 / Gemini / Groq / OpenRouter）③ 自定义 / 冷门平台连接（自建网关、代理站、小众厂商） | 点平台 → 地址与默认模型自动填好 → 粘 API Key → 保存 |
| **本地** | ② 系统扫描本机（扫本机端口，看有没有在跑的 AI）③ 已知本地应用（点一下直接填好，不用扫描）④ 自定义本地服务（扫不到就手填） | 扫描 → 点「连接」回填 / 直接点应用 / 手填 → 选协议 → 保存 |

两条分支必须分得开：「云端」下面是「要密钥」，「本地」下面是「多数不要密钥」。
把本机应用混进云端清单，**位置本身就在骗人**。

模型名让服务端自己报（`llm.v1.models` 支持 OpenAI 兼容的 `GET /models` 与 Ollama 的 `/api/tags`，
比在客户端硬编码一份迟早过期的清单靠谱）。焦点自动落在密钥框上，粘完点保存即生效：

```
┌─ AI 工作平台 · 工作台 ──────────────────────────────────────────── core v1.0.0 · com.official.workbench ─┐
│ 聊天    插件    文件   [设置]   角色    重启                                     云端 · provider：openai │
│ ① 接入方式  ● 云端     本地                                 │ API Key  留空=不改动；减号=清空            │
│   云端：连公网上的大模型服务，需要 API Key                  │ API Key   [▏已配置 sk-l****7890         ]  │
│                                                             │    密钥在 platform.deepseek.com/api_keys…  │
│ ② 知名平台  点一下，地址与模型自动填好                      │                                            │
│ ● DeepSeek     Kimi      智谱 GLM      通义千问             │ 模型  点按钮要一份真实清单                 │
│   硅基流动      OpenAI      百度文心      腾讯混元          │ ● deepseek-chat     deepseek-reasoner      │
│   字节豆包      讯飞星火      MiniMax      阶跃星辰         │  获取可用模型                              │
│   零一万物      Google Gemini      Groq      OpenRouter     │                                            │
│   DeepSeek · OpenAI 兼容 · https://api.deepseek.com/v1      │ 端点与协议  协议选错会连不上               │
│   密钥在 platform.deepseek.com/api_keys 申请                │ ● OpenAI 兼容     Ollama 原生              │
│                                                             │   离线 mock                                │
│ ③ 自定义 / 冷门平台  自建网关 / 代理站                      │ Base URL  [https://api.deepseek.com/v1  ]  │
│   自定义 / 冷门平台                                         │ 模型      [deepseek-chat                ]  │
│   选它之后，到右栏「端点与协议」里填 Base URL 与模型名（可  │                                            │
│   点「获取可用模型」照抄）。密钥按该服务的要求填。          │ 识图模型  带图片时改用这个                 │
│                                                             │ 识图模型  [留空 = 跟主模型一样          ]  │
│                                                             │                                            │
│                                                             │  保存并生效    测试连接                    │
│                                                             │                                            │
│                                                             │ 工具调用预算  超了自动收手；也可不限       │
│                                                             │ 工具上限  [6                            ]  │
│                                                             │ ● 限制步数     不限制                      │
│                                                             │  应用预算                                  │
├─────────────────────────────────────────────────────────────┴────────────────────────────────────────────┤
│ 当前 provider：openai                                                                                    │
└─ 点平台 → 地址与模型自动填好 → 粘密钥 → 保存 · 「自定义 / 冷门平台」里手填地址与模型 · Tab 换控件 · 滚… ─┘
```

本地那一栏，**扫不到不算失败** —— 多数机器上本来就没有本机 AI 服务，所以那句话是中性提示加出路
（不标红），手填入口常驻在下面。可连的格式与各自默认端口直接摊在图例里，一眼就能对照自己的应用：

```
┌─ AI 工作平台 · 工作台 ──────────────────────────────────────────── core v1.0.0 · com.official.workbench ─┐
│ 聊天    插件    文件   [设置]   角色    重启                                     本地 · provider：openai │
│ ① 接入方式    云端    ● 本地                                │ API Key  留空=不改动；减号=清空            │
│   本地：连这台机器上跑的模型，多数不要密钥                  │ API Key   [▏已配置 sk-l****7890         ]  │
│                                                             │    密钥在 platform.deepseek.com/api_keys…  │
│ ② 系统扫描本机  看这台机器上有没有 AI                       │                                            │
│  扫描本机  扫描了 11 个常见本地端点，发现 2 个在运行：…     │ 模型  点按钮要一份真实清单                 │
│ ✓ Ollama Ollama 原生 · 2 个模型   连接                      │ ● deepseek-chat     deepseek-reasoner      │
│   http://127.0.0.1:11434 · 端口 11434 · 3ms                 │  获取可用模型                              │
│   模型 llama3.1 · qwen2.5                                   │                                            │
│ ✓ LM Studio OpenAI 兼容 · 1 个模型   连接                   │ 端点与协议  协议选错会连不上               │
│   http://127.0.0.1:1234/v1 · 端口 1234 · 12ms               │ ● OpenAI 兼容     Ollama 原生              │
│   模型 ai/ai/qwen-qwen3.5-9b.gguf                           │   离线 mock                                │
│                                                             │ Base URL  [https://api.deepseek.com/v1  ]  │
│ ③ 已知本地应用  点一下直接填好，不用扫描                    │ 模型      [deepseek-chat                ]  │
│   Ollama      LM Studio      vLLM      llama.cpp            │                                            │
│   text-generation-webui      KoboldCpp      Jan             │ 识图模型  带图片时改用这个                 │
│   GPT4All      SGLang      Xinference      离线 mock        │ 识图模型  [留空 = 跟主模型一样          ]  │
│   可连的格式（默认端口）——Ollama:11434 · LM Studio:1234     │                                            │
│   vLLM:8000 · llama.cpp:8080 · text-gen-webui:5000          │  保存并生效    测试连接                    │
│   KoboldCpp:5001 · Jan:1337 · GPT4All:4891 · SGLang:30000   │                                            │
│   Xinference:9997                                           │ 工具调用预算  超了自动收手；也可不限       │
│                                                             │ 工具上限  [6                            ]  │
│ ④ 自定义本地服务  扫不到就手填                              │ ● 限制步数     不限制                      │
│     格式不受限：只要它说的是 OpenAI 兼容（/v1）或 Ollama 原 │  应用预算                                  │
│     生（/api），这里就能连。到右栏「端点与协议」选好协议，  │                                            │
│     再填 Base URL 与模型名。                                │                                            │
├─────────────────────────────────────────────────────────────┴────────────────────────────────────────────┤
│ 当前 provider：openai                                                                                    │
└─ 先点「扫描本机」看看有没有现成的 · 扫不到就在「自定义本地服务」里手填 · 点「连接」回填 · Tab 换控件 ·… ─┘
```

**协议跟着端点走，不跟着「本地」这个词走。** 右栏「端点与协议」三个选项对应三套接口：
`OpenAI 兼容`（`/v1/chat/completions` —— LM Studio / vLLM / llama.cpp / LocalAI / one-api / 自建网关都算）、
`Ollama 原生`（`/api/chat`）、`离线 mock`（不连网、不要密钥，用来验证整条链路）。
选错的两种表现都不给线索：Ollama 被按 OpenAI 问会回 404 纯文本，而 LM Studio 被按 Ollama
问更绝 —— 回 **HTTP 200** 加一句 `{"error":"Unexpected endpoint or method."}`，状态码检查根本拦不住。

**窄窗口也能存。** 窗口够宽时这一页分两栏；窄下来就并成一栏竖排，此时整页自成一屏可滚（滚轮），
不会出现「保存并生效」永远落在屏幕之外、配置根本存不下去的情况。

**粘贴是安全的。** 密钥里混进制表符、换行是常事，而制表符在界面里正好是「切页」键。
界面用两种方式把「粘贴」和「逐键输入」分开：Windows 认 `VK_PACKET`，其余平台用括号粘贴
（`ESC[?2004h` … `ESC[?201~`）。粘贴进来的控制字符一律丢弃，**绝不会把用户弹回聊天页**。

**保存之后看得见。** 保存成功后输入框会清掉明文、换上「已配置 sk-l****7890」，底部状态栏再确认一次
「密钥已写入」—— 密钥不停留在屏幕上，但你一眼能看出它确实存上了。**保存失败则密钥原样留在输入框里**，
不用重新粘一遍；后台回读配置也不会覆盖你正在输入的内容（这两个都是用户反馈后改的：
原来无论如何都清空，看着就像没存上）。

| 鼠标 | 行为 |
|---|---|
| 单击 | 标签页、列表项、按钮、输入框 |
| 滚轮 | 聊天页翻历史 / 插件页滚动列表 / 设置页翻页（窗窄时它并成一栏，比一屏长） |

| 按键 | 行为 |
|---|---|
| `Tab` | 设置页正在输入时：换到下一个输入框/按钮；其余情况：切页（聊天 → 插件 → 文件 → 设置） |
| `↑` `↓` | 插件页换选中 / 设置页换焦点 / 聊天页翻历史 |
| `Enter` | 聊天页发送；设置页焦点在按钮上就是按下它，在输入框上则移到下一个；插件自带的一页则由那一页自己决定（它收到的是 `enter` 这个语义） |
| `←` `→` `Home` `End` `Delete` `Backspace` | 编辑输入 |
| `Esc` | 清空输入、取消聚焦（取消后 Tab 才恢复成切页） |
| `Ctrl+L` | 清空对话与事件 |
| `Ctrl+C` | 关闭整个平台（界面会先还原终端，不会留下坏终端） |

鼠标按平台各走一条路：Windows 控制台用 `ReadConsoleInputW` 的原生鼠标事件（**VT 输入不转发鼠标**，
所以光发转义序列收不到点击），Unix 与重定向场景用 xterm SGR 鼠标上报 —— 两条路归一到同一个事件模型。
为了让点击能到达程序，Windows 下会关掉控制台的**快速编辑模式**，代价是这个界面运行时不能拖选复制文本。

因为插件的 stdin/stdout 是 JSON-RPC 硬通道，界面**绕开它们**直接渲染控制终端（Windows `CONIN$`/`CONOUT$`，Unix `/dev/tty`）。

### 极简 UI（保底界面）

`minimal-ui` 仍然随仓库提供，作用是**任何终端上都能跑**：不依赖鼠标，且在拿不到原始模式时自动降级。
两个 UI 都提供 `ui.v1`，所以默认只把工作台装进 `plugins/`；`minimal-ui` 打包在 `runtime/builtin/` 里留用，
格式化后引导菜单装回来的就是它。

界面分两档，自动选择：

- **全屏 TUI**（终端可交互）：备用屏 + 原始模式，边框分栏、CJK 对齐、方向键编辑、异步转发不卡界面
- **行式界面**（拿不到原始模式时）：逐行问答，永不失败

```
┌─ AI 工作平台 · 极简 UI ───────────────────────────────────────────── core v1.0.0 · apiVersion 1 ─┐
│ 系统 14:32:01                                                         │服务链路                  │
│    欢迎使用 AI 工作平台。                                             │ ✗ session.v1             │
│    直接输入文字即发送；输入 /help 查看命令。                          │    未安装                │
│                                                                       │ ✓ echo.v1                │
│ 你   14:32:10                                                         │    com.official.echo     │
│    读一下 runtime/core.json                                           │ ✓ readFile.v1            │
│                                                                       │    com.official.fs-to…   │
│ AI   14:32:10                                                         │ ✗ llm.v1                 │
│    {                                                                  │    未安装                │
│      "apiVersion": "1",                                               │ ✗ sandbox.v1             │
│      "controlPort": 9123                                              │    依赖缺失              │
│    }                                                                  │                          │
│    经由 readFile.v1 返回                                              │事件总线                  │
│                                                                       │ 14:32:11 chat.message …  │
│ ⠸ 正在转发…                                                           │ 14:32:11 core.log {"le…  │
│                                                                       │                          │
├───────────────────────────────────────────────────────────────────────┴──────────────────────────┤
│ 你 › 帮我再读一下 go.mod                                                                         │
└─ Enter 发送 · ↑↓ 历史 · /help 命令 · Ctrl+C 退出界面 ────────────────────────────────────────────┘
```

| 按键 | 行为 |
|---|---|
| `Enter` | 发送 |
| `↑` `↓` | 翻阅历史输入 |
| `←` `→` `Home` `End` `Delete` | 编辑光标 |
| `Ctrl+L` | 清空对话 |
| `Esc` | 清空当前输入 |
| `Ctrl+C` | 关闭整个平台（界面会先还原终端，不会留下坏终端） |

| 命令 | 说明 |
|---|---|
| `/help` | 显示帮助 |
| `/services` | 重新探测服务链路 |
| `/tool <service> <method> [json]` | 调试：直接 `core.invoke` 任意服务 |
| `/events on\|off` | 开关侧栏的事件总线显示 |
| `/clear` | 清空对话与事件 |
| `/exit` | 退出界面（核心继续运行） |

`/tool` 让整个平台可自省，也能直观验证能力代理：

```
你 › /tool readFile.v1 run {"path":"runtime/core.json"}
结果 › {
  "content": "{ \"coreVersion\": \"1.0.0\", ... }",
  "path": "E:\\...\\runtime\\core.json",
  "size": 276,
  "truncated": false
}
     readFile.v1.run（1ms）
```

界面的排版有硬性不变量：**每一行的显示宽度精确等于终端宽度**（含中文、emoji 的双列宽度）。
`go test ./plugins/minimal-ui` 在 8 种尺寸 × 有无颜色共 16 种组合下逐一校验；
`go test ./plugins/workbench` 除 5 种尺寸 × 3 个页面外，还额外校验「无颜色渲染下不得出现任何转义序列」——
这条不变量抓出过一次真 bug：详情面板把样式码拼进了文本，既被算进显示宽度，又原样打到了屏幕上。

把 `fs-tool` 的 `permissions` 改成 `[]` 再调用同一个服务，得到的是：

```
你 › /tool readFile.v1 run {"path":"runtime/core.json"}
错误 › [-32002] 权限不足：插件 com.official.fs-tool 未在 manifest 中声明 filesystem:read
```

### 触发格式化（应急通道）

```sh
# Linux / macOS
kill -USR2 <pid>

# 全平台：本地端口 + Token（Token 在 runtime/core.json 的 controlToken 字段）
curl -X POST -H "X-Format-Token: <token>" http://127.0.0.1:9123/format
```

格式化会：停止调度 → 杀死所有插件 → 清空 `plugins/` 与 `plugin-configs/` → 重置内存表 → 保留 `core` / `core.json` / `logs/` / `builtin/` → 重启并进入引导菜单。

格式化后 `plugins/` 是空的，引导菜单只能装回官方极简 UI。要恢复其余官方插件，用引导菜单的 `install <路径>`，或直接重跑一次构建脚本（`build.ps1` / `make build`）。

---

## 想做什么，在哪找

省掉"我到底该装哪个插件"的来回翻找：

| 你想做的事 | 在哪 | 要装插件吗 |
|---|---|---|
| 聊天 | 工作台的「聊天」页 | 不用（默认都装好了） |
| **让 AI 看懂图片** | 工作台的「附件」按钮，或把文件拖进终端 | **不用** —— 见下文「上传附件」一节 |
| **让 AI 画矢量图**（图标/图示/招牌） | `/tool svgdraw.v1 draw {"prompt":"…"}` | 不用 —— 你现在的文本模型就能画 |
| **让 AI 画照片级图片** | `/tool imagegen.v1 generate {"prompt":"…"}` | **要装 `imagegen`**，并配一个图像服务 |
| **看还剩多少钱、烧了多少 token** | 用量浮窗（能拖、能收、位置记住），或 `/tool usage.v1 snapshot` | 不用（默认装了） |
| **看 AI 现在在想什么、调了什么工具** | 消息区里的「AI 在做什么」过程框 | 不用（默认装了） |
| 让 AI 看图时换个模型 | 工作台「设置」→「识图模型」 | 不用 |
| 改模型 / 密钥 / 接口地址 | 工作台「设置」页 | 不用 |
| 删掉某个插件 | `/tool pluginManager.v1 delete {"id":"…"}` | 不用 |
| 让 AI 自己写插件 | `/tool builder.v1 …` | 不用（默认装了） |

> **「识图」不是一个插件。**
>
> 它是「llm 插件的**识图模型**配置」+「工作台的**附件上传**」两样配合出来的，
> 所以插件列表里**找不到**一个叫「识图」的东西，那是正常的。
> 反过来，「画图」是一件独立的事（另一个服务商、另一套计费），所以它才是独立插件 `imagegen`。

---

## 源码结构

```
万物皆插件/
├── README.md
├── go.mod                     # 单模块：核心 / SDK / 官方插件共用
├── Makefile  build.ps1        # Unix / Windows 构建入口
│
├── core/                      # 核心源码（不含任何构建产物）
│   ├── main.go                #   入口 + 参数解析
│   ├── host/                  #   宿主内核：粘合层 + 6 个基础接口 + 生命周期 + 信号
│   ├── loader/                #   扫描、manifest 解析、版本范围、依赖拓扑排序、解压、pending-restart
│   ├── process/               #   子进程管理、JSON-RPC over stdio 收发、状态机
│   ├── registry/              #   服务注册表 + 事件总线 + 命名规范校验
│   ├── capability/            #   能力代理：文件 / 网络 / 子进程，权限闸门 + 路径围栏 + 审计
│   ├── config/                #   插件配置持久化
│   ├── format/                #   格式化应急通道（信号 / 本地端口 Token / re-exec）
│   ├── bootstrap/             #   引导菜单 TUI（核心内置，不可插件化）
│   ├── logs/                  #   日志落盘
│   └── jsonrpc/               #   JSON-RPC 2.0 消息模型与错误码
│
├── sdk/
│   └── go/plugin/             # Go 插件 SDK（import "aiwork/sdk/go/plugin"）
│
├── launcher/                  # 启动器（唯一一个“不干正事”的程序，见下一节）
│   ├── main.go                #   找 runtime/core、查重、拉起、转发退出码
│   ├── console_windows.go     #   Windows：改控制台窗口标题（其余平台是空实现）
│   ├── name.txt               #   产品名（UTF-8，构建脚本和快捷方式脚本都读它）
│   └── assets/appicon.png     #   图标原图
│
├── tools/                     # 构建辅助（都是独立 main 包，不进任何产物）
│   ├── icon2ico/              #   原图 -> 多尺寸 .ico（裁白边、抠背景、盒式缩放）
│   ├── set-windows-resources.ps1  # 往 exe 里写图标 + 版本信息（UpdateResource）
│   └── install-shortcut.ps1   #   在桌面 / 开始菜单放一个快捷方式
│
├── plugins/                   # 官方插件源码（构建后落到 runtime/plugins/<名字>.plugin/）
│   ├── fs-tool/               #   readFile.v1 / writeFile.v1 / listDir.v1 —— 能力代理示范
│   ├── workspace/             #   workspace.v1 —— 指定落脚文件夹（相对路径基准），绝对路径可访问整机
│   ├── llm/                   #   llm.v1 —— 模型接入（mock / OpenAI 兼容 / Ollama）
│   ├── tool-executor/         #   toolExecutor.v1 —— 工具目录与执行
│   ├── agent/                 #   agent.v1 —— 推理 → 工具 → 再推理的编排循环
│   ├── plugin-manager/        #   pluginManager.v1 —— 列出 / 检查 / 启停 / 删除 / 创建插件
│   ├── builder/               #   builder.v1 —— 编译 AI 写的 Go 插件（唯一声明 process:spawn 的插件）
│   ├── session/               #   session.v1 —— 多轮会话历史
│   ├── conversation/          #   conversation.v1 —— 链式对话：消息树 / 版本翻页 / 多会话
│   ├── imagegen/              #   imagegen.v1 —— 文生图（光栅图，需外部图像服务）
│   ├── svgdraw/               #   svgdraw.v1 —— 文本画矢量图（让模型写 SVG，不需要外部服务）
│   ├── usage-monitor/         #   usage.v1 —— 用量悬浮窗：DeepSeek 余额 / token / 缓存命中率
│   ├── workbench/             #   ui.v1 —— 可点击的工作台界面（默认界面；看图 / 发图都在这里）
│   └── minimal-ui/            #   ui.v1 —— 终端聊天界面（保底界面，自带行式降级）
│
└── runtime/                   # 全部构建产物与运行时数据（.gitignore）
    ├── core[.exe]             #   核心二进制
    ├── app.ico                #   图标（由 launcher/assets/appicon.png 生成）
    ├── core.json              #   核心配置（格式化不删除）
    ├── builtin/minimal-ui.zip #   内置官方 UI 包（格式化保留）
    ├── plugins/               #   插件目录（格式化清空）
    ├── plugin-configs/        #   插件配置（格式化清空）
    ├── logs/                  #   日志（格式化保留）
    └── pending-delete/        #   待删除插件（重启后清理）
```

**一条硬约定：源码树里不放任何构建产物。** 编译产物、解压出来的插件、日志、内置包全部落在 `runtime/`，删掉 `runtime/` 再跑一次构建就能完全复原。

`启动AI工作平台.exe` 是这条约定唯一的例外，它落在项目根目录而不是 `runtime/`：
它的全部意义就是**位置显眼、双击可用**，塞回 `runtime/` 里就等于没做。
它同样是构建产物（`.gitignore` 里的 `*.exe` 覆盖得到），删掉也能重新构建出来。

`plugins/<名字>/` 是源码目录，`runtime/plugins/<名字>.plugin/` 是构建产物目录，中间由构建脚本搬运（含给入口加 `.exe`、补 `.plugin` 后缀）。

---

## 发布下载包（发给别人装）

```powershell
powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1          # 构建 + 打包
powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1 -Version 1.1.0
powershell -ExecutionPolicy Bypass -File tools\pack-release.ps1 -SkipBuild  # 用现有 runtime 直接打
```

产物是 `dist/ai-work-platform-<版本>.zip`（外加同名 `.sha256.txt`）。对方拿到后：

1. 解压到任意位置；
2. 右键 `install.ps1` →「使用 PowerShell 运行」，弹出文件夹选择窗口，**装到 C/D/E 哪块都行**；
3. 装完自动建桌面 + 开始菜单快捷方式，双击图标即用；
4. 卸载：运行安装目录里的 `uninstall.ps1`。

**包里刻意不含任何个人数据**，打包脚本会硬校验、命中即中止：

| 不带走 | 原因 |
| --- | --- |
| `runtime/core.json` | 含本机控制令牌；不带它，核心首次启动会自己生成一个随机令牌 |
| `runtime/plugin-configs/*` | 含模型 API Key、会话记录、工作区路径 |
| `runtime/logs/*` | 运行日志 |
| 白名单外的插件 | 我本地装的试验插件不会被误带走 |

白名单就是官方插件那几个（`fs-tool`、`llm`、`tool-executor`、`agent`、`session`、`conversation`、`workbench`、`plugin-manager`、`workspace`、`builder`、`imagegen`、`svgdraw`、`usage-monitor`）。控制令牌由核心现场生成，API Key 由使用者自己在界面里填。所有 `go build` 都带 `-trimpath`，可执行文件里不留构建机路径。

---

## 启动器与图标

核心是一千多行、看不见摸不着的加载器，但用户面对的是「**一个能双击的东西**」。
所以外面单独放了个 `launcher`，只有一百多行，做四件事：

1. 站在项目根目录、用产品名命名、带图标 —— 找得到、看得出是什么
2. 把控制台窗口标题改成产品名。核心只管往 stderr 打一行启动信息；窗口是启动器开的，标题自然归启动器管
3. 启动前探一下 `core.json` 里那个控制端口。已经在跑就直接说清楚，而不是再开一个实例去和它抢终端、抢插件目录里的 exe
4. 子进程共用同一个控制台，然后等它退出、原样转发退出码。共用一个控制台是有意的：关窗口时 Windows 会把挂在控制台上的进程一起带走，不会留下孤儿 core

它**不认识**插件、不认识 JSON-RPC、不认识 UI —— 和核心守的是同一条边界。

图标以 `launcher/assets/appicon.png` 为原图，`tools/icon2ico`（纯标准库，零第三方依赖）
生成 16/24/32/48/64/128/256 七档 `.ico`：

| 步骤 | 为什么 |
|---|---|
| 裁掉白边、再补回内边距 | 原图把图案顶到画布边上了，直接缩到 16x16 时齿会被切掉一圈 |
| 从四条边漫水抠白底 | 只抠**和边缘相连**的白；图案内部的白色块不能动 |
| 预乘 alpha 的盒式滤波缩放 | 不预乘的话，蓝色边缘和透明背景混完会糊出一圈白边 |

`.ico` 里每一档存的是 32bpp BMP 而不是 PNG：BMP 载荷资源编译器、老版资源管理器、
`UpdateResource` 都认；PNG 载荷只有 Vista 之后的 Explorer 认。

整条链路是 `appicon.png → app.ico → UpdateResource`。最后一步走 Windows 自带的
`BeginUpdateResource` / `UpdateResource` / `EndUpdateResource`，因为 **Go 的链接器
不往 PE 里写 `.rsrc` 段**：少了这一步，exe 在资源管理器里就是白板图标，
右键「属性 → 详细信息」里的产品名和版本号也全是空的。

顺带一个真踩到的坑：**exe 正在运行时资源写不进去**（文件被占用，`EndUpdateResource` 直接失败）。
构建脚本把这一步包在 `try` 里，失败只 `Write-Warning` 不中断构建 —— 平台开着的时候构建，
出来的东西照样能用，只是图标没换。

---

## 核心暴露的 6 个基础接口

这是核心的**全部**对外能力。

### ① 服务注册与发现

```json
{"jsonrpc":"2.0","id":1,"method":"core.provide","params":{"name":"echo.v1","meta":{}}}
{"jsonrpc":"2.0","id":2,"method":"core.consume","params":{"name":"echo.v1"}}
{"jsonrpc":"2.0","id":2,"result":{"pluginId":"com.official.echo","alive":true}}
```

服务名必须带版本。提供者卸载后，消费者调用自动失败（`-32001`）。

### ② 服务调用

```json
{"jsonrpc":"2.0","id":3,"method":"core.invoke","params":{
  "service":"echo.v1","method":"run","args":{"text":"hello"}
}}
```

核心收到后转发给目标插件，并把**响应 id 还原给调用者**：

```json
// 核心 → echo 插件
{"jsonrpc":"2.0","id":7,"method":"core.invoke","params":{
  "service":"echo.v1","method":"run","args":{"text":"hello"},
  "caller":"com.official.minimal-ui","forwarded":true
}}
```

规则：不允许自调用服务（会返回 `-32603`）；提供者已死返回 `-32001`；核心暂停调度（格式化中）返回 `-32603`。

### ③ 事件总线

```json
{"jsonrpc":"2.0","method":"core.emit","params":{"event":"chat.message","payload":{}}}
{"jsonrpc":"2.0","id":4,"method":"core.on","params":{"event":"chat.message"}}
{"jsonrpc":"2.0","method":"core.event","params":{"event":"chat.message","payload":{},"origin":"com.official.minimal-ui"}}
```

事件名必须带命名空间；`"*"` 可订阅全部。插件卸载时核心自动取消其所有监听。事件不持久化，不保证跨插件顺序。

订阅这件事有**两半**，缺一不可：SDK 的 `ctx.On` 必须既把处理函数记在本地，又向核心发一次 `core.on`。
核心的 `Emit` 只投给 bus 里登记过的订阅者，所以少发那一次请求的话，插件会**一个事件都收不到** ——
而它看起来完全正常：服务照常注册、`ctx.Emit` 照常发得出去、日志里没有任何错误，只是 `ctx.On` 的回调永远不被调用。
这个缺陷在 SDK 里真实存在过（`ctx.On` 当时只做了前一半），所有插件的 `ctx.On` 都是死的；
现在补上了，并且有专门的用例盯着这一行（`sdk/go/plugin/server_test.go`）。
**注意：改的是 SDK，所以插件必须用修好的 SDK 重新构建才会带上这个修复。**

### ④ 配置管理

```json
{"jsonrpc":"2.0","id":5,"method":"core.getConfig","params":{"key":"port"}}
{"jsonrpc":"2.0","id":6,"method":"core.setConfig","params":{"key":"port","value":8090}}
```

每个插件独立命名空间，落在 `plugin-configs/<pluginId>.json`。核心只读写，不解释内容，键只增不减。

### ⑤ 日志

```json
{"jsonrpc":"2.0","method":"core.log","params":{"level":"info","msg":"activated","meta":{"took":"12ms"}}}
```

核心只写入 `logs/<pluginId>.log`，同时广播 `core.log` 事件供日志插件接管。核心不分析、不过滤、不告警。插件 stderr 也会被转发到同一日志文件。

### ⑥ 沙箱（接口预留）

```json
{"jsonrpc":"2.0","id":7,"method":"core.sandbox.run","params":{"code":"...","options":{"timeout":5000}}}
```

核心只定义接口，不实现。未安装沙箱插件时返回 `-32003 sandbox.unavailable`；装了则转发给 `sandbox.v1` 服务的 `run` 方法。

另有两个**第二阶段预留**接口：`core.getSecret` / `core.setSecret`，未安装 `secrets.v1` 插件时返回 `-32000`。

---

## 插件自带界面（ui.page.*）

**装一个带界面的插件，界面就多一页 —— UI 插件一行都不用改。**

这是"万物皆插件"落到界面上最要紧的一步。以前每加一个功能插件，都要在 UI 插件里
加常量、加字段、加按钮、加动作分支（`workbench` 里曾经有一整页专为 workspace 写的
「文件」页，还有一页专为 roleplay 写的「角色」页），界面于是成了所有插件的大杂烩。

### 分工

```
插件   自己把画面画成「文本行 + 可点区域」，自己处理点击与按键
界面   只借一块 w×h 的地方、把点击和按键转过去、把行原样贴上去
```

界面里没有一行代码知道"工作区""角色扮演"是什么，它只认识四条规则：

1. 送来的行不能比给定宽度宽（中文、emoji 占两列）
2. 可点区域用面板内坐标
3. 动作名与参数原样送回插件
4. 内容变了要喊一声，否则界面会停在旧画面上

### 怎么提供一页

在 manifest 的 `provides` 里加一个 **`ui.page.<名字>.v1`**，在它上面实现四个方法：

| 方法 | 参数 | 返回 |
|---|---|---|
| `list` | — | `{panels:[{id,title,order}]}`：我有哪几页 |
| `render` | `{panel,w,h,scroll}` | `{title,lines,hits,scroll,maxScroll,note,wantKeys}`：一帧画面 |
| `action` | `{panel,action,arg}` | `{note}`：插件自己定义的动作，界面不解析 |
| `key` | `{panel,kind,text}` | `{ok}`：`kind` 是 `rune` / `backspace` / `enter` / `up` 这类**语义**，不是转义字节 |

为什么不用统一的 `ui.page.v1`：服务名在注册表里是**一对一**的，第二个声明同名服务的
插件会被拒 —— 那"自带界面"就只容得下一个插件。名字里带上插件自己的名字才没有这个冲突。

Go 插件直接用 SDK 里的绘制原语（`sdk/go/plugin/frame.go`）：宽度计算、边界截断、
单行文本框、列表窗口都在里面，插件不必自己算终端列数。

```go
b := plugin.NewBlock(w, h)                        // 一块恰好 w×h 的画面
b.At(0).Text(plugin.SBold, "我的插件").Fill(plugin.SDim)

b.At(1).Text(plugin.SDim, " 名字 [")
caret := b.At(1).Col()
used := f.Draw(b.At(1), 20, true, "")             // 文本框：光标、滚动、点击定位都在里面
b.At(1).Text("", strings.Repeat(" ", 20-used))
b.At(1).Text(plugin.SDim, "]")
for col := 0; col < 20; col++ {
	b.Hit(1, caret+col, 1, "caret", strconv.Itoa(col)) // 点第几列就把光标放到第几列
}
b.Button(2, 1, "保存", "save", "", true)

out := b.Render(scroll, maxScroll, "底部那句提示")  // 交给 ui.page.*.render 返回
out.WantKeys = true                               // 这一页要键盘
return out, nil
```

### 界面这边给了什么、扣下了什么

- **给**：一块 `w×h` 的正文区、点击（含坐标）、按键、滚轮；上一帧的 `scroll` 原样传下去
- **扣**：`Tab`（切页）永不转给插件 —— 那是用户仅有的退路，交给插件的话，
  一个写坏的页面就能把人困在里面。`Ctrl+C` 是复制（页面声明 `WantCopy` 就转给它复制），
  `Ctrl+Q` 是退出，两件事分开
- **不认识的**：不解析动作名、不知道页面内容、不管插件的状态存在哪里
- **兜底**：插件画歪了（行宽不对）就剥掉样式按纯文本重排；还没拿到帧就画占位；
  渲染出错也缓存下来**只试一次**（不然每帧都在打插件）

### 插件没了，页面就没了

这一条是"自带界面"能不能成立的关键：**插件删掉之后，它的页面不能有一丝残留**。
清干净要连做四件事，少一件都会留下痕迹：

1. **`plugin.deleted` 必须订阅**。界面靠事件重扫插件目录；不订阅的话删除这个动作
   根本传不到界面耳朵里，标签会一直挂着。
2. **判据是"带页面的那组服务变过"，不是"清单为空"**。后者是早期为接住启动竞态写的
   （插件逐个激活，界面可能先起来），但插件一删清单就不空了 —— 于是再也没人问第二次。
3. **帧缓存与在途请求一起作废**。只删缓存不够：一个"删除之前"发出的请求回来，
   会被当成新帧存下，下一帧照它画，屏幕上就还是那一页。
4. **热区也要清**。屏幕上那一块已经不在可点区域的范围里了，还留着热区的话，
   点下去就是把动作发给一个已经不在的插件。
5. **删除是重启生效的，目录此刻还在**。正在运行的插件文件被占用、删不掉，插件管理器
   只是标了"待重启删除"。所以光靠"重扫"不够 —— 扫描照样能看到那个目录、把它的页面
   加回来。界面得在收到 `plugin.deleted` 时**立刻**把它的页面摘掉，并记住"它被删了"，
   之后每一轮扫描结果都把它滤掉（这个窗口期只到重启：重启时核心会真把目录删干净，
   下次扫描自然看不到）。
6. **事件不是唯一的信号，还要周期兜底重扫**。直接删插件目录、或者事件在订阅之前就
   丢了，`plugin.deleted` 根本到不了界面。所以界面每隔 3 秒主动扫一次插件目录 ——
   扫到"上次有、这次没了"的插件，就按上面的链路把它的页面摘掉。扫描很轻，3 秒一次
   用户无感，却能让"删了却还赖着"的页面最多多活 3 秒。

正在看着那一页时插件被删，界面直接退回聊天并说明原因 —— 比留一块空白好。
插件崩溃、被禁用之后重启，走的也是同一条链路（服务没了 → 页面跟着没）。

### 现在的样子

| 插件 | 自带的一页 |
|---|---|
| `plugin-manager` | 「插件」—— 清单 / 详情 / 启停 / 删除（删除前把会动哪些文件摊开）；「规则」—— 插件开发规范（服务名 / 事件名 / 权限名等命名规则） |
| `workspace` | 「工作区」—— 浏览目录、挑工作目录 |
| `roleplay` | 「角色」—— 角色列表 / 角色卡 / 我的身份 / 提示词预览 |
| `llm` | 「模型」—— 平台预设 / 本机扫描 / 密钥 / 协议 / 识图模型 |
| `agent` | 「预算」—— 工具调用上限 / 不限制 |
| `imagegen` | 「文生图」—— 服务商预设 / 密钥 / 模型 / 尺寸 |

`workbench` 里原本硬编码的四页，现在只剩「聊天」这一页 —— 对话是界面存在的理由，
不属于任何插件。其余六页（插件 / 工作区 / 角色 / 模型 / 预算 / 文生图）全部由各自的
插件自带：界面真的只是一个"借地方、转发点击与按键"的合成器，装一个带界面的插件
就多一页，这个插件一行都不用改。

**不是每个插件都该有一页**：`fs-tool` / `svgdraw` 是无头工具（AI 经工具调用它们，
没有"配一配、看一看"的面）；`conversation` 的界面就是聊天页本身；`builder` 是纯构建
服务（编译结果直接回给调用方，没有可配置项）；`usage-monitor` 的界面是它自己的悬浮窗
（一个浮动小窗，不是标签页那种"页"）。所以这四家都不开标签页。

**「模型」页搬走时顺手解掉一个更深的重复**：`presets.go` 那张 20 条服务目录
（地址 / 默认模型 / 密钥申请地址 / 本机端口）原本长在界面插件里，而 llm 插件
自己就有 `providers` / `discover` / `configure` —— 同一份知识存了两份。现在目录
进了 llm 插件，还配了一条单测锁住"目录里的本机应用与扫描候选表不能飘开"。

**搬走一页之后，界面那边删掉的是什么**（以插件页为例）：`tabPlugins` 常量、
`pluginListRows`/`pluginDetailRows` 一屏渲染、`selectPlugin`/`toggleSelected`/
`deleteSelected` 一套动作、`setPluginEnabled`/`deletePlugin` 两个转发方法、
`selectedPlugin`/`pluginBusy`/`confirmDelete` 等状态字段，以及它专用的那十几个用例。
界面从此不认识"启停插件"这四个字，只认识 `ui.page.*`。

**界面这边其实还留着一件事**：它仍然会扫一遍 `plugins/` 目录读各家的 manifest。
这不是为了画插件页（那已经搬走了），而是因为要认出**哪些插件提供了自带页面** ——
核心没有"列出所有已注册服务"的接口（规范 1.5 只有 6 个），而插件清单是唯一能看到
`provides` 的地方。顺带它还用来回答"侧栏里这个服务此刻是谁在提供"。

---

## 能力代理（规范 3.5）

第一阶段是**协作式**隔离：插件不自己动手，把意图交给核心，核心按 manifest 里声明的权限执行。

| 能力方法 | 所需权限 | 参数 | 返回 |
|---|---|---|---|
| `core.fs.read` | `filesystem:read` | `{path, limit?, encoding?}` | `{path, size, content, truncated, encoding?}` |
| `core.fs.list` | `filesystem:read` | `{path}` | `{path, entries:[{name, dir, size}]}` |
| `core.fs.write` | `filesystem:write` | `{path, content, append?, mkdir?}` | `{path, size, append}` |
| `core.fs.delete` | `filesystem:delete` | `{path, recursive?}` | `{path, existed, deleted, moved, pending?}` |
| `core.fs.move` | `filesystem:write` | `{from, to}` | `{from, to, moved}` |
| `core.fs.mkdir` | `filesystem:write` | `{path, recursive?}` | `{path, created, existed}` |
| `core.net.request` | `network` | `{method, url, headers?, body?, timeoutMs?, encoding?}` | `{status, headers, body, truncated, encoding?}` |
| `core.shell.open` | `shell:open` | `{path}` | `{path, opened}` |
| `core.process.spawn` | `process:spawn` | `{command, args?, dir?, timeoutMs?}` | `{exitCode, stdout, stderr, truncated}` |

规则：

- **默认拒绝**：权限没在 manifest 的 `permissions` 里声明，一律返回 `-32002 权限不足`
- **权限随插件生命周期**：启动时按 manifest 授予，插件死亡时立即收回，格式化时全部清空
- **路径围栏（可选）**：`core.json` 的 `fsRoot` 非空时，文件访问必须落在该根目录内，用 `..` 或符号链接绕出都会被拒绝；**留空（默认）则可访问整机任意路径**
- **路径解析**：绝对路径直接使用；相对路径固定解析到 `fsRoot`（若配置）或运行时目录，**绝不跟随核心进程的当前目录**——那取决于运维怎么启动核心，是不确定行为
- **审计**：每次能力调用写一行 `logs/core.log`，例如 `能力代理：com.official.fs-tool 调用 core.fs.read E:\...\core.json`
- **输出上限**：单次调用最多 4 MB，`core.fs.read` 默认最多 1 MB（可用 `limit` 调整）
- **网络**：只允许 `http` / `https`，只允许 `GET/POST/PUT/PATCH/DELETE/HEAD`
- **删除与文件锁**：`core.fs.delete` **先整体移进 `pending-delete/` 再删副本**（这一步是原子的）；
  连改名都被拒时**原样保留**并返回错误。绝不做"能删几个删几个"——那会留下一个 manifest 没了、入口还在的残疾目录
- **护栏**：不允许删除运行时目录本身，也不允许删除它的一级子目录（`plugins/` / `logs/` / …）。要删请删到具体条目

需要说明的是，插件**仍可以绕过代理直接调用系统 API**——那是「不可信代码」问题，由第二阶段的沙箱插件负责。核心保证「插件不会轻易搞死核心」，沙箱插件保证「不可信代码不会搞死插件」。

`core.json` 相关配置（格式化不删除）：

```json
{
  "fsRoot": "",
  "netTimeoutMs": 30000,
  "spawnTimeoutMs": 30000
}
```

---

## 把核心变成 AI 工作站

核心依然不认识 AI。下面是四个普通插件，装齐之后链路才通：

| 插件 | 提供 | 职责 | 依赖 |
|---|---|---|---|
| `llm` | `llm.v1` | 把消息 + 工具定义翻译成模型调用 | 核心能力代理（声明 `network`） |
| `tool-executor` | `toolExecutor.v1` | 维护工具目录，向模型暴露 JSON Schema，按名字执行 | 各工具插件（软依赖） |
| `agent` | `agent.v1` | 编排：推理 → 调工具 → 再推理，直到给出最终回答 | `llm.v1` 硬依赖，`toolExecutor.v1` 软依赖 |
| `session` | `session.v1` | 多轮历史，界面的第一入口 | `agent.v1` → `llm.v1` 逐级降级 |

装齐这四个，**文字对话 + 工具调用 + 看图**就通了（看图靠 `llm` 的识图模型 + 界面的附件上传，**没有也不需要单独的"识图插件"**）。

以下这些按需再加，不加也不影响上面那条链路：

| 插件 | 提供 | 干什么 | 什么时候需要 |
|---|---|---|---|
| `conversation` | `conversation.v1` | 消息树 / 版本翻页 / 多会话 | 想要"改一条消息就重开一条时间线" |
| `workbench` | `ui.v1` | 可点击的界面（聊天、附件、缩略图、模型接入都在这儿），并把插件自带的一页（`ui.page.*`）合成进标签栏 | 想用鼠标点（默认界面） |
| `workspace` | `workspace.v1` | 指定落脚文件夹（相对路径基准）；**自带一页界面**：浏览目录、挑工作目录 | 想让 AI 读写文件（通常都要） |
| `roleplay` | `roleplay.v1` | 角色卡 / 我的身份 / 长期记忆；**自带一页界面**：角色列表、编辑、提示词预览 | 想让 AI 换个身份说话 |
| `plugin-manager` | `pluginManager.v1` | 列出 / 检查 / 启停 / 删除插件；**自带两页界面**：清单 / 详情 / 启停 / 删除，以及插件开发规则 | 想管理插件 |
| **`imagegen`** | `imagegen.v1` | **文生图：文字进、图片出**；**自带一页界面**：服务商 / 密钥 / 模型 / 尺寸 | **想让 AI 画图**（要另配图像服务，见下文） |
| **`usage-monitor`** | `usage.v1` | **用量悬浮窗：余额 / token / 缓存命中率** | 想知道这一轮花了多少钱（见下文） |
| `builder` | `builder.v1` | 编译 AI 写的 Go 插件 | 想让 AI 自己造插件 |
| `fs-tool` | `readFile.v1` 等 | 模型调用的读/写/列目录工具 | 想让 AI 动文件（通常都要） |

### 一次对话的完整链路

```
你在界面输入「读一下 _demo.txt」
  → ui.v1          core.invoke session.v1.sendMessage
  → session.v1     core.invoke agent.v1.run（带历史）
  → agent.v1       core.invoke toolExecutor.v1.list      （拿到 5 个工具的 schema）
  → agent.v1       core.invoke llm.v1.chat（带 tools）
  → llm.v1         经 core.net.request 出网（或走离线 mock）
  ← 模型要求调用 read_file
  → agent.v1       core.invoke toolExecutor.v1.execute
  → toolExecutor    core.invoke readFile.v1.run
  → fs-tool        core.fs.read（能力代理校验 filesystem:read）
  ← 文件内容原路返回
  → agent.v1       把结果作为 tool 消息追加，再调一次 llm.v1.chat
  ← 模型给出最终回答
  ← session.v1 存历史 → ui.v1 渲染
```

**核心在这条链路里只做了一件事：转发 JSON-RPC。**

### 零配置先跑起来

不配任何东西也能把整条链路跑通：`llm.v1` 在没检测到密钥时自动选离线 `mock` provider。
链路、工具调用、事件、历史持久化全都是真实的，只有最外面那一次「模型推理」是本地模拟。

```
你 › 读一下 _demo.txt
AI › 工具执行完成，结果如下：

     文件 E:\...\runtime\_demo.txt：
     第一行：这是给 agent 读的示例文件。
     …
     经由 session.v1 返回
```

想看工具循环就对它说「读一下 <路径>」；想精确指定工具，用 `tool:<工具名> <json参数>`。

### 接真实模型

**不需要重启**（核心每次 `core.getConfig` 都从磁盘读，下一次调用就生效）：

```
你 › /tool llm.v1 configure {"provider":"ollama","model":"llama3.1"}
你 › /tool llm.v1 configure {"provider":"openai","apiKey":"sk-…"}
你 › /tool llm.v1 configure {"provider":"openai","baseUrl":"https://api.deepseek.com/v1","apiKey":"sk-…","model":"deepseek-chat"}
```

也可以直接编辑 `runtime/plugin-configs/com.official.llm.json`，或设环境变量
`OPENAI_API_KEY` / `OPENAI_BASE_URL` / `AIWORK_LLM_PROVIDER`。

`provider=openai` 覆盖**任何** OpenAI 兼容接口（OpenAI / DeepSeek / Moonshot / vLLM / LM Studio / one-api…）；
`provider=ollama` 走本地原生接口。密钥优先级：插件配置 → 环境变量 → 秘密存储插件（装了 `secrets.v1` 时经 `core.getSecret` 取）。

### 接本机跑的模型（LM Studio / Ollama）

界面里点「自定义本地服务」（或云端那一栏的「自定义 / 冷门平台」）走的就是这条路。
关键原则一句话：**provider 要跟着服务说的协议走，不是跟着"本地"这个词走。**

| 本机服务 | provider | 地址 | 模型名 |
|---|---|---|---|
| LM Studio | `openai` | `http://127.0.0.1:1234/v1` | 点「获取可用模型」；它的 id 可能长这样：`ai/ai/qwen-qwen3.5-9b.gguf` |
| vLLM / one-api / llama.cpp | `openai` | 各自的 `/v1` | 同上（`GET /models`） |
| Ollama | `ollama` | `http://127.0.0.1:11434` | 点「获取可用模型」（走 `/api/tags`） |

四个最容易卡住的点：

1. **协议选错**（最隐蔽）：地址对着 LM Studio，provider 却留在 `ollama`。插件会拿 `/api/chat`
   去打一个只认 `/v1/chat/completions` 的服务，而 LM Studio 的回答是 **HTTP 200** 加
   `{"error":"Unexpected endpoint or method."}` —— 状态码完全拦不住，用户只看到"连不上"。
   现在这种回应会被认出来，错误里直接写明该把 provider 改成哪个。
2. **模型名要照抄**：LM Studio 的 id 可能是 `ai/ai/<文件名>.gguf`，简写成 `qwen3.5-9b` 会得到
   `400 model_not_found`。点「获取可用模型」从清单里挑，别手打。
3. **LM Studio 一次只常驻一个模型**：切模型会卸载旧的，此时还在用旧模型的请求会拿到
   `400 {"error":"Model unloaded."}`。小机器上尤其要注意 —— 4.36 GB 的能跑，5.29 GB 的可能就带不动。
4. **本机服务也可能要密钥**（这条件曾经写反过，坑了人）：LM Studio 有一个「要求 API 令牌」开关，
   在 **Developers → Server Settings** 里，**默认是关的**。一旦打开，所有请求都必须带
   **它自己生成**的令牌，而密钥框里留着的往往是云端的密钥 —— 于是每个请求都回 401，
   报错还只有一句"接口拒绝了这个密钥"。两条出路：把那个开关关掉，或者
   **Manage Tokens → Create Token** 建一个令牌**立刻复制**（官方明确说令牌只显示一次，之后再也看不到），
   粘进右栏的 API Key 框。现在这类失败会附带上面这段说明，不用再自己摸。
   另外记住：**密钥框是全平台共用的** —— 填了本机令牌，切回云端服务时要再换成云端的密钥。

密钥**多数**情况下留空即可，但别默认"本机一定免鉴权"（见第 4 条）。注意**留空表示"不改动"**，
想主动清掉密钥就在框里输入一个减号。

### 用 `/tool` 逐层自省

```
你 › /tool llm.v1 providers                       # 当前 provider、掩码后的密钥、各后端默认值
你 › /tool llm.v1 models                          # 向服务端要一份真实可用的模型清单
你 › /tool toolExecutor.v1 list {"availableOnly":true}
你 › /tool agent.v1 info                          # 依赖是否就绪、步数/时间预算、系统提示词
你 › /tool session.v1 history
你 › /tool agent.v1 run {"messages":"tool:now {}"}
```

### 工具是怎么来的

`toolExecutor.v1` 的出厂目录把已有服务桥接成模型看得懂的工具：

| 工具名 | 落到哪个服务 |
|---|---|
| `read_file` | `readFile.v1.run` |
| `write_file` | `writeFile.v1.run` |
| `list_dir` | `listDir.v1.run` |

**这三个工具的相对路径默认落在用户指定的工作区里**，而不是运行时目录。
不这么做的话，模型写 `write_file {"path":"notes.txt"}` 会掉到 `core.exe` 旁边那个目录里 ——
用户在界面上挑好的文件夹形同虚设。绝对路径则一律原样使用。
工作区还没指定时保持原行为（核心解析到运行时目录）。

（工作区由 `workspace.v1` 决定，见下面的「工作区文件」一节。`fs-tool` 只是软依赖它：
没装工作区插件也不影响读写文件，只是相对路径回到运行时目录。）
| `now` | 内置，不依赖任何插件 |

工具插件没装时，`list` 会如实返回 `available:false` 和原因（`未安装` / `依赖缺失` / `版本不兼容`），
而不是让工具凭空消失 —— 一眼能看出还差哪个插件。

加工具不用改代码，注册结果会持久化到插件配置，重启仍在：

```
你 › /tool toolExecutor.v1 register {"name":"my_tool","description":"…","service":"xxx.v1","method":"run","schema":{…}}
```

工具插件若想在启动时自注册，在 manifest 里声明对 `com.official.tool-executor` 的 `dependencies` 即可保证启动顺序。

### 三个刻意的取舍

- **缺插件不崩**：`agent` 没有 `toolExecutor` 就退化为纯聊天；`session` 没有 `agent` 就直连 `llm`。
  只有连 `llm.v1` 都没有才报错，并在错误里说清楚该装哪个插件。
- **预算可自定义，也可以不限制**：默认单次对话最多 6 轮工具调用、240 秒。
  模型反复调同一个工具是常态，没预算会一直烧 token；但"最多几轮"该由用户决定，
  不该由插件写死 —— 所以三个预算值都能改，**设成 `-1` 就是不限制**：

  ```
  你 › /tool agent.v1 configure {"maxSteps":40}      # 自定义：最多 40 轮
  你 › /tool agent.v1 configure {"maxSteps":-1}      # 不限制：由模型自己决定何时停手
  你 › /tool agent.v1 configure {"timeoutMs":-1}     # 不限时长
  你 › /tool agent.v1 configure {"maxToolResultChars":-1}  # 工具结果不截断
  你 › /tool agent.v1 info                           # 看当前预算（不限制会明确写出来）
  ```

  也可以在工作台「设置」页的「④ 工具调用预算」里点：填一个数字，或选「不限制」，
  再点「应用预算」。改完立即生效，且重启后仍记得。
- **错误喂回模型**：工具失败不直接抛给用户，而是把错误当作工具结果交回去，
  让模型自己看懂原因、改参数重试 —— 这才是工具调用真正有用的地方。

### 事件

`agent` 每一步都会发事件，界面里 `/events on` 即可看到：
`agent.step`、`agent.done`、`chat.tool.before`、`chat.tool.after`、`session.updated`。

---

## 插件规范

### 包结构

```
my-plugin.plugin/
├── manifest.json          # 必填
├── main                   # 入口可执行文件（任何语言编译产物）
└── assets/                # 静态资源（可选）
```

或打包成 `my-plugin.zip` 放进 `plugins/`，核心启动时自动解压为 `my-plugin.plugin/` 并删除 zip。

### manifest.json

```json
{
  "id": "com.example.echo",
  "name": "Echo Tool",
  "version": "1.0.0",
  "apiVersion": "1",
  "entry": "main",
  "runtime": "binary",
  "dependencies": { "com.official.sandbox": "^1.0" },
  "provides": ["echo.v1"],
  "consumes": ["llm.v1"],
  "permissions": ["network", "filesystem:read"],
  "compat": { "core": ">=1.0 <2.0" }
}
```

`runtime` 支持 `binary` / `python` / `node` / `java` / `wasm`（`wasm` 需要适配器插件）。
解释器路径可用 `AIWORK_PYTHON` / `AIWORK_JAVA` / `AIWORK_NODE` 覆盖。

版本范围支持 `>=1.0 <2.0`、`^1.0`、`~1.2`、`1.0.0`、`*`。

### 启动协议

核心启动插件子进程后，第一件事是发送 `core.init`（**固定使用 id 0**）：

```json
{"jsonrpc":"2.0","id":0,"method":"core.init","params":{
  "pluginId":"com.official.echo","apiVersion":"1","coreVersion":"1.0.0",
  "configNamespace":"com.official.echo","permissions":[]
}}
```

插件必须在 **5 秒**内响应，否则核心杀死该插件并标记失败：

```json
{"jsonrpc":"2.0","id":0,"result":{"ready":true}}
```

### 生命周期与状态

| 阶段 | 触发 | 插件行为 |
|---|---|---|
| 启动 | 核心发送 `core.init` | 初始化，注册服务，订阅事件 |
| 运行 | 核心转发 `core.invoke` / `core.event` | 处理调用与事件 |
| 退出 | 核心发送 `core.shutdown` | 清理资源，可选择退出 |
| 强杀 | 超时或格式化 | 无 |

状态：`pending` / `starting` / `active` / `degraded` / `dead` / `disabled`。
插件崩溃时核心记录日志、标记 `dead`、注销其服务与监听，**不自动重启（MVP）**，也不影响核心与其他插件。

### 错误码

| 错误码 | 说明 |
|---|---|
| `-32000` | 服务未找到 |
| `-32001` | 服务提供者已死 |
| `-32002` | 权限不足 |
| `-32003` | 沙箱不可用 |
| `-32004` | 依赖缺失 |
| `-32005` | apiVersion 不兼容 |

### 安装 / 删除 / 更新

所有变更**重启生效**，不热更新。删除与更新先进 `pending-delete/`，重启后被清理；Windows 上文件被锁导致删除失败时也会移入 `pending-delete/`。

---

## 让 AI 自己做插件

平台可以自我扩展：**AI 能给自己造插件**。写完重启，新能力就长在它身上了。
（上一节的例子里 `calc` 就是这么来的。）

### 一条命令造一个插件

```
/tool pluginManager.v1 create {
  "id": "com.ai.calc",
  "name": "Calc",
  "description": "安全求值数学表达式",
  "runtime": "python",
  "provides": ["calc.v1"],
  "code": "... 完整的 Python 源码 ...",
  "tools": [
    {"name": "calc", "method": "run", "description": "算数学表达式，需要算数时用它",
     "schema": {"type":"object","properties":{"expr":{"type":"string"}},"required":["expr"]}
  ]
}
```

`create` 负责所有**格式**问题，AI 只给 id 和代码：

| 它替你做的事 | 说明 |
|---|---|
| 建目录 | `plugins/<名字>.plugin/`，名字由 id 最后一段推出（`com.ai.calc` → `calc.plugin`） |
| 生成 manifest | 必填字段一个不少，取值也按核心的校验规则来（见下表） |
| 校验入参 | id 必须反向域名、version 必须合法、权限名必须小写冒号、服务名必须带 `.vN` |
| 注册 AI 工具 | 传了 `tools` 就自动调 `toolExecutor.v1.register`，模型下次对话就能用 |
| 告诉下一步 | `nextStep` 明说：python 直接重启；go 要先编译 |

返回里的 `nextStep` 会告诉你接下来做什么，不用猜。

### 插件格式（create 生成的就是这份）

```
runtime/plugins/<名字>.plugin/
├── manifest.json     # 必填
└── main.py           # 入口（runtime=python 时）
```

`manifest.json` 的字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | ✅ | 反向域名，至少两段：`com.ai.calc`。**不符合会被核心跳过** |
| `name` | ✅ | 人类可读的名字 |
| `version` | ✅ | 语义化版本。宽松接受 `v1.2` / `1.0` / `1.0.0-beta.1`，写进文件时会规范化成 `1.2.0` |
| `apiVersion` | ✅ | 固定 `"1"` |
| `entry` | ✅ | 入口文件名。`python` 默认 `main.py`；`binary` 默认 `main`（Windows 自动找 `.exe`） |
| `runtime` | ✅ | `binary` / `python` / `node` / `java` / `wasm` |
| `compat.core` | ✅ | 版本范围，`create` 固定写 `">=1.0 <2.0"` |
| `provides` | | 提供的服务，**必须带版本后缀**：`calc.v1` |
| `consumes` | | 会调用哪些服务 |
| `permissions` | | `filesystem:read` / `filesystem:write` / `filesystem:delete` / `network` / `process:spawn`。**没声明的能力一律 `-32002`** |
| `dependencies` | | 声明启动顺序依赖的插件 ID |

### 两种语言怎么选

| | Python | Go |
|---|---|---|
| `runtime` 字段 | `python` | `binary`（源码是 `main.go`，编译后是原生可执行文件） |
| AI 能独立完成吗 | **能**，写完重启即可 | 源码能写，**编译要调 builder** |
| create 时传 | `"runtime": "python"` | `"runtime": "go"` |
| 之后 | 重启 → 加载 | `builder.v1 build {"dir":"calc.plugin"}` → 重启 → 加载 |

Python 骨架**自包含**（不 import SDK，自己实现 JSON-RPC），所以放到任何机器上都能跑；
Go 骨架用官方 SDK，性能好，但必须有 Go 工具链。

不确定机器上有什么，先问一句：

```
/tool builder.v1 toolchains    # → go / python / node / javac / gcc 哪些可用
```

### 关于 builder 的安全边界

编译 Go 需要执行进程，所以 `builder` 是**唯一声明 `process:spawn` 的插件**。
但它**不接受任何命令参数**：

- 只能说"构建某个插件目录"，命令 `go build`、参数、工作目录全部由它写死
- 目标目录必须匹配 `<名字>.plugin` 这个形状，`../` 之类一律拒绝
- 带超时，失败时如实返回 exitCode / stdout / stderr

也就是说：拿到的是**编译器**，不是 shell。AI 借它编译不了别的东西。

### 装完之后

1. **重启核心** —— 规范：所有变更重启生效
2. 界面「插件」页能看到新插件，也能一键**禁用 / 删除**（AI 装的，你随时能拆）
3. 如果传了 `tools`，AI 对话时会自动调用它

## 工作区文件（workspace）

### 在界面上指定（工作区插件自带的一页）

终端里没有系统文件对话框，所以「选」被做成了一个**可点的目录浏览器**，
而且**填**这条路一直留着 —— 两条路最后都落到同一个地方。

这一页**由 workspace 插件自己画**（见下文「插件自带界面（ui.page.*）」）：
它把画面画成"文本行 + 可点区域"，界面只借一块地方、把点击和按键转过去。
所以改这一页不用动界面插件，换个 UI 插件它也照样在。

```
┌─ AI 工作平台 · 工作台 ───────────────── core v1.0.0 · com.official.workbench ─┐
│ 聊天    插件   设置   角色   [工作区]                        插件页面 · 工作区 │
│ 工作区文件  当前：E:\万物皆插件\runtime                                       │
│ 路径  [E:\万物皆插件\runtime▏                                             ]   │
│  设为工作区  刷新  向上一级                                                   │
│ ──────────────────────────────────────────────────────────────────────────── │
│ 目录 E:\万物皆插件\runtime  7 项                                              │
│  ▸ builtin                                                                    │
│  ▸ logs                                                                       │
│  · core.exe                                                 11.4 MB           │
│  · core.json                                                276 B             │
├───────────────────────────────────────────────────────────────────────────────┤
│                                                                               │
└─ 点路径可直接改 · 点目录进入 · 回车设为工作区 · Tab 切页 ──────────────────────┘
```

| 操作 | 怎么做 |
|---|---|
| 直接填 | 进这一页输入框就是热的，粘贴或打字，**回车**即生效 |
| 点着挑 | 点目录名**进入**，点「向上一级」**返回**，走到目标点「设为工作区」 |
| 看大小 | 文件后面显示人类可读的大小（`11.4 MB`）；目录有 `▸` 前缀 |
| 找不着 | 「刷新」重读当前目录；标题行一直显示当前工作区 |

浏览到的目录会**自动回填进输入框** —— 点着挑和手填看到的永远是同一个路径，
不会出现"我明明点进去了，框里却写着别的地方"。

工作区只有真正点「设为工作区」（或回车）才算数：光浏览不会改变任何东西，
所以随便逛逛是安全的。

### 在终端里用 `/tool`

**指定一个落脚文件夹，然后整机哪里都能折腾**——创建、读取、修改、移动、复制、删除，
文件和目录都行。`fs-tool` 是按绝对路径读单个文件的示例，这个才是日常能用的那一个。

```
你 › /tool workspace.v1 setRoot {"path":"E:\\my-project"}   # 可选：只决定相对路径落在哪
你 › /tool workspace.v1 root                                 # 看当前工作目录（重启后仍记得）
你 › /tool workspace.v1 browse {"path":"E:\\"}              # 列任意目录，用来挑工作区

你 › /tool workspace.v1 write  {"path":"notes/a.txt","content":"第一行"}
你 › /tool workspace.v1 write  {"path":"notes/a.txt","content":"\n第二行","append":true}
你 › /tool workspace.v1 read   {"path":"notes/a.txt"}
你 › /tool workspace.v1 mkdir  {"path":"src/utils"}
你 › /tool workspace.v1 copy   {"from":"notes/a.txt","to":"notes/b.txt"}
你 › /tool workspace.v1 move   {"from":"notes/b.txt","to":"src/b.txt"}
你 › /tool workspace.v1 delete {"path":"src","recursive":true}

你 › /tool workspace.v1 list                                 # 列一层
你 › /tool workspace.v1 list   {"recursive":true}            # 递归列出全部
你 › /tool workspace.v1 tree                                 # 目录树（默认 3 层）
你 › /tool workspace.v1 stat   {"path":"notes/a.txt"}
```

**工作目录只是相对路径的落脚点**：相对路径拼到它下面，绝对路径原样使用 ——
整机任意位置（含 C/D/E 各盘符）都能读写，不再有盘符 / 越界围栏。

| 写法 | 结果 |
|---|---|
| `notes/a.txt` | 解析到 `<工作目录>/notes/a.txt` |
| `C:\Users\me\Desktop\a.txt` | 原样使用（绝对路径直通） |
| `D:\data\x.txt` | 原样使用（跨盘符已不再拦截） |
| `../core.json` | 相对路径 → `<工作目录的上一级>/core.json`（也不再拦） |

唯一还兜底挡住的是**核心运行时目录**：核心的能力代理拒绝删除它本身或它的一级子目录
（`plugins/` / `logs/` / …），要删请删到具体条目。工作目录本身也不能被删掉。

`setRoot` 的校验方式是让核心去列一次这个目录：列得出来就说明它存在、是目录、且有权限。
存下来的是核心返回的绝对路径，所以相对路径也能用，之后一律以它为基准。
工作目录写进插件配置，重启后不用重设。

两个已知限制：

- `copy` 按 **UTF-8 文本**复制（内容要经 JSON 传一趟），二进制文件请用 `move`
  —— 改名不经过内容传输，对二进制是安全的
- `list {"recursive":true}` 最多 2000 条、`tree` 最多 400 个条目，超了会截断并标注

---

## 删除插件（plugin-manager）

删除本身也是一个插件 —— 核心没有、也不会有"管理插件"的接口，它只提供原子能力。

`com.official.plugin-manager` 提供 `pluginManager.v1`：

```
你 › /tool pluginManager.v1 list                                    # 已安装插件（含 disabled / self 标记）
你 › /tool pluginManager.v1 inspect {"id":"com.official.fs-tool"}   # 详情 + 会删哪些文件 + 谁会受影响
你 › /tool pluginManager.v1 delete {"id":"com.official.fs-tool","dryRun":true}
你 › /tool pluginManager.v1 delete {"id":"com.official.fs-tool"}
你 › /tool pluginManager.v1 delete {"id":"com.official.fs-tool","keepLogs":true}
你 › /tool pluginManager.v1 disable {"id":"com.official.fs-tool"}   # 停用（改目录名，重启后不再加载）
你 › /tool pluginManager.v1 enable  {"id":"com.official.fs-tool"}   # 启用
你 › /tool pluginManager.v1 purge                                   # 手动清空 pending-delete/
```

**在工作台界面上也能点**：切到「插件」页，点任意一个插件，右侧详情里就有
「启用 / 禁用」和「删除插件」两个按钮。删除不可逆，所以点一下先变成
「确认删除 + 取消」，再点一次才真的动手；换选中插件或按 `Esc` 都会撤掉确认。
插件管理器和界面自己受保护，不提供这两个按钮。

一次 `delete` 动三样东西，缺一样都不算干净：

| 目标 | 路径 |
|---|---|
| 插件目录 | `plugins/<name>.plugin/` |
| 插件配置 | `plugin-configs/<id>.json` |
| 插件日志 | `logs/<id>.log`（`keepLogs:true` 保留） |

### 启用 / 禁用

核心只扫描 `*.plugin` 结尾的目录，所以**启用与禁用就是改目录名**：

```
demo.plugin  ←→  demo.plugin.disabled
```

不新增任何文件，也不需要核心提供"禁用"接口。改完同样记一条 pending 记录，
核心在下次启动、加载插件之前执行 —— 因为运行中的插件目录被占用，连改名都会被拒。

| 场景 | 结果 |
|---|---|
| 插件没在运行 | 当场改名成功，`changed:true` |
| 插件正在运行 | 改不动，记 pending 后返回 `deferred:true`，下次启动时生效 |

### 删除正在运行的插件：先热卸载，删不掉再退回重启删除

Windows 的规矩：**可执行文件被进程占用时，既不能删，连改名都会被拒**。
而 `logs/<id>.log` 被核心的日志器持有，同样动不了。

所以"删一个插件"是**先热卸载、再删文件、删不动才标 pending**：

1. **先热卸载**（`core.unload`）：插件正在运行就先停掉它的进程、注销服务、清订阅 —— 这是规范 2.6"重启生效"的例外，因为卸载（往下）比加载（往上）安全。卸载成功，下面目录就能当场删掉，用户不用再重启一次
2. 卸载没成功（进程赖着不走 / 没在运行），就照旧删文件：目录**原样保留**，返回 `status:"deferred"` —— 绝不做"能删几个删几个"
3. 配置当场删掉（它没被占用）
4. 删不干净的才往 `pending-restart.json` 写一条 `delete` 记录
5. **下次启动时，核心在加载任何插件之前执行删除** —— 此刻没有进程占用、日志还没打开，删得干干净净

```
[INFO] 已清除待删除项 E:\...\runtime\plugins\demo.plugin
[INFO] 已清除待删除项 E:\...\runtime\plugin-configs\com.example.demo.json
[INFO] 已清除待删除项 E:\...\runtime\logs\com.example.demo.log
```

启动清理会核对 `by` 字段：发起方必须仍然安装着、且 manifest 里声明了 `filesystem:delete`。
只有 `filesystem:write` 的插件写一条记录就想让核心替它删东西，会被拒绝并记警告。

### 删之前先说清楚后果

`inspect` 与 `delete` 都会在 `warnings` 里列出：

- 谁正在 `consumes` 它的服务（例如删 `fs-tool` 会提示 `tool-executor` 将降级）
- 谁在 `dependencies` 里声明了它（重启后可能因依赖缺失被跳过）
- 它是否提供 `ui.v1` —— 删掉并重启后没有界面，需要引导菜单装回 `minimal-ui` 或 `workbench`

删除自己是不允许的：`delete {"id":"com.official.plugin-manager"}` 返回 `-32602`。

---

## 写一条消息：多行输入，点「发送」送出

**回车是换行，不是发送。** 这是刻意的：写长消息时敲个回车就发出去，回头想补一句
就得整段重打一遍 —— 而"回车即发送"和"多行输入"本来就只能二选一。

| 按键 | 行为 |
|---|---|
| 回车 / Shift+回车 | 插入一个换行。输入框会长高，最多 6 行，再长就跟着光标滚动 |
| Ctrl+Enter | 发送（键盘用户的送出口，手不用离开键盘） |
| 点「发送」 | 发送。**修改态这个按钮写「提交」** —— 同一个位置做的是"把这条存成新版本" |
| ↑ ↓ | 输入框不止一行时：在行之间走光标；只有一行时仍然是原来的"选消息框 / 滚历史" |
| Home / End | 当前显示行的行首 / 行尾（只有一行时与"整个输入的首尾"完全等价） |
| Esc | 放弃修改（修改态） |

折行只影响**显示**：只有你按下的回车才是内容。窗口拉窄会把一行折成好几行，
但发出去的消息里没有那些软折 —— 否则窗口一窄，消息内容就跟着变了。

「发送」按钮的位置是算出来留给它的：文字的折行宽度会**预先扣掉按钮那几列**，
所以文字再长也压不住按钮（压住了热区还在、人却看不见，那才是最难查的一类问题）。

---

## 上传附件：让 AI 看文件与图片

聊天页输入行右侧有「附件」和「发送」两个按钮。附件有三种进法，殊途同归：

| 方式 | 怎么用 |
|---|---|
| 点按钮 | 点「附件」→ 面板占住聊天区（左挑右看）→ 点文件名加入，点目录进去 → 点「完成」回到聊天 |
| 拖进终端 | 把文件拖到窗口里，终端会插入路径 → 回车，自动认成附件（**不会**当场发出去） |
| 粘路径 | 粘贴 `"C:\my docs\a.png"` 后回车，同上 |

附件随这一条消息一起走（发出去永远是点「发送」或 Ctrl+Enter）：

```
workbench → conversation.v1 / session.v1 → agent.v1 → llm.v1
```

### 三种附件，三种处理

| 类型 | 判断依据 | 怎么交给模型 |
|---|---|---|
| 图片 | `.png .jpg .jpeg .gif .webp .bmp` | 读成 base64 的 data URL，作为**多模态 image part** —— 模型真的"看见" |
| 文本 | `.txt .md .go .py .json .csv …` | 内容直接拼进正文 |
| 其它 | 视频 / 音频 / 压缩包 … | 只报文件名与大小，并**如实告诉模型"无法解析内容"** |

视频这类东西目前的模型读不了，与其让它对着二进制瞎猜，不如说清楚。等你问"这个文件多大""叫什么名字"时它照样答得上来。

### 图片走 base64，不是走 string

`core.fs.read` 默认按 UTF-8 转字符串，二进制里的无效字节会被换成 `U+FFFD` —— 图片就毁了。所以加了 `encoding: "base64"`：

```jsonc
// 读图片（二进制安全）
{"method":"core.fs.read","params":{"path":"cat.png","encoding":"base64"}}
// → {"path":"…","size":68,"encoding":"base64","content":"iVBORw0KG…","truncated":false}
```

超过 3 MB 的附件会被拒绝而不是截断发送 —— 半个文件发给模型，它只会一本正经地胡说。

### 附件不进会话历史

一张图的 base64 动辄几 MB，存进会话文件会把树撑爆、翻页变卡。所以：

- 历史里只留一行 `[附件: cat.png]`，看得出当时附了什么
- 附件内容只参与**这一次**请求
- 发完自动清空，下一条消息不会莫名又带上同一张图

---

## 角色扮演：角色卡 / 我的身份 / 记忆（roleplay 插件）

「角色」页里管三样东西：

| 是什么 | 内容 |
|---|---|
| 角色卡 | 名字 / 人物设定 / 性格 / 场景 / 开场白 —— 都是可编辑的文本 |
| 我的身份 | 你自己是谁（名字 + 设定）。**对所有角色生效**，编辑入口就在角色卡下面 |
| 记忆 | 这个角色"记得"的事，一条一行，可以一直往后加 |

这一页**由 roleplay 插件自己绘制**（`ui.page.roleplay.v1`，见
[插件自带界面（ui.page.*）](#插件自带界面uipagev1)）：界面只借一块地方给它，
画什么、点了怎样、键落在哪儿全在插件里。键盘是一条"环" —— ↑↓ 在
按钮、角色列表、字段之间走，回车落下去，打字进当前字段（Tab 被界面拿去切页了）。

点「启用」之后，回聊天页说话的就是它了；「停用」会把 agent 原本的系统提示词**原样还回去**。

### 它是怎么接进对话的

现有链路（界面 → conversation.v1 → agent.v1 → llm.v1）**不认识"角色"这个概念**，它只认系统提示词。
所以角色扮演的落地办法是**翻译**：

```
角色卡 + 我的身份 + 记忆
  → roleplay.v1 拼装
  → agent.v1.configure {systemPrompt}     ← 启用时推过去，停用/删除时还回来
  → 之后每条消息都带着它（链路一行都不用改）
```

改动在启用中立刻生效：改完点「保存角色」，下一条消息就按新人设说。
记忆和身份同理 —— 它们都是提示词的一部分，不重新推的话模型下一句就忘了刚才交代的事。

### 记忆可以由对话产生

插件会向 `toolExecutor.v1` 注册一个 **`remember`** 工具：模型需要记下什么（称呼、约定、经历）
就自己调它。记忆不是让用户手抄的，是聊出来的。

"记住"必须落到工具上：只在嘴上说"我记住了"没有任何用，下一轮它就忘了。

### 三个细节，都是踩出来的

- **模型写记忆 ≠ 你覆盖记忆**：保存角色卡时，如果你**没动过**记忆那一栏，界面就不提交它。
  否则会出现这个场面 —— 模型刚在对话里记住一条，你回角色页顺手点一下「保存」，那条就没了。
（这一条在角色页搬进插件之后仍然成立，而且更直白了：页面只在"记忆框真的被改过"时才把
`memory` 提交上来，见 `plugins/roleplay/panel.go` 里 `dirty` 的说明。）
- **记忆栏显示的是末尾**。记忆只会往后长，新记的在最后；显示开头的话，你刚记住的事永远在框外，
  看起来像没记上。想清空就在框里输入一个减号（与设置页同一个约定）。
- **换角色无条件覆盖表单**：哪怕你正在改（还没保存），点另一个角色也会整块换掉 ——
  留着上一个角色的内容，你会对着错的设定改，保存下去就把另一张卡写坏了。

依赖是软的：没有 `agent.v1` 也能编辑（只是推不出去），没有 `toolExecutor.v1` 也能用
（只是模型没法自己写记忆）。两种情况都会写进日志，而不是静默失效。

---

## 文本画图（svgdraw）：不接外部服务也能出图

先说清一个常见误解：**"把文本改个后缀就是图片"是行不通的**。

```
把 "这是一段文字" 存成 fake.png →
  前 8 字节：e8 bf 99 e6 98 af e4 b8
  真 PNG 必须是：89 50 4e 47 0d 0a 1a 0a（魔数）
  解码结果：image: unknown format
```

改后缀只换了文件名，字节一个没变。

但**"让模型用文本画图"确实有正路 —— SVG**。SVG 不是"把图编码成文本"，而是**文本本身就是图形指令**：

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 600 400">
  <rect width="600" height="400" fill="#DA251D" />        <!-- 红旗底 -->
  <path d="M…" fill="#FFDE00" />                           <!-- 五角星 -->
</svg>
```

`com.official.svgdraw` 干的正是这件事：

```
/tool svgdraw.v1 draw {"prompt":"一面红旗，左上角一颗黄色五角星，简洁扁平风格"}
/tool svgdraw.v1 draw {"prompt":"一个蓝色圆角按钮，写着「开始」","name":"btn"}
/tool svgdraw.v1 registerTool     # 注册成 draw_svg，AI 能自己决定"我画一个给你看"
```

链路是：`prompt → llm.v1（模型写 SVG）→ 抠出 <svg>…</svg> → 存成 .svg`。**不需要任何外部图像服务，不需要额外的 key** —— 你已经在用的那个文本模型就够了。

### 两个画图插件怎么分工

| | `svgdraw` | `imagegen` |
|---|---|---|
| 产出 | **矢量图**（SVG） | **光栅图**（PNG） |
| 擅长 | 图标、图示、招牌、图表、简单插画 | 照片级写实图 |
| 需要外部服务 | **不需要** | 需要（且要 key） |
| 放大 | 不糊 | 会糊 |

矢量图放大不糊，画示意图和图标最合适；照片那种它无能为力 —— 那是 `imagegen` 的活。

### 两个坑（都真踩过）

**一、模型爱自作主张包一层。** 它会写"好的，这是你要的图："再给 ```xml 围栏。指望 prompt 里叮嘱没用（它照样犯），所以 `extractSVG` 老老实实把两端裁掉，并且拒绝半截内容（只有 `<svg` 没有 `</svg>`）。

**二、思考模型会把 token 花光。** `deepseek-flash` 默认开 thinking，预算给小了正文就是空的（实测 1200 时吐出 0 字节）。所以调用里固定给 `maxTokens: 8000`。

---

## 生成图片（imagegen，独立插件）

`com.official.imagegen` 提供 `imagegen.v1`。它**刻意不塞进 llm** —— llm 管"文字进、文字出"，这里管"文字进、图片出"，两者的服务商、模型、密钥、计费都不一样。

```
/tool imagegen.v1 generate {"prompt":"一只橘猫坐在窗台上，水彩"}
/tool imagegen.v1 providers
/tool imagegen.v1 configure {"provider":"siliconflow","apiKey":"sk-…"}
/tool imagegen.v1 registerTool     # 注册成 generate_image，让 AI 能自己决定"我要画一张"
```

对接的是 OpenAI 兼容的 `POST /images/generations`，所以这些都能直接填：

| 服务 | baseUrl | 默认模型 |
|---|---|---|
| OpenAI | `https://api.openai.com/v1` | `dall-e-3` |
| 阿里通义万相 | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `wanx-v1` |
| SiliconFlow | `https://api.siliconflow.cn/v1` | `Kwai-Kolors/Kolors` |
| 智谱 CogView | `https://open.bigmodel.cn/api/paas/v4` | `cogview-3` |
| 本地 SD / ComfyUI | `http://127.0.0.1:7860/v1` | `sd` |

生成的图存进**工作区**的 `generated-images/`（没设工作区就存运行时目录），文件名带 prompt 摘要与时间戳。**返回的是绝对路径**，所以界面会自动把它画成缩略图 —— "AI 生图"到"你看得见图"是接上的。

### 为了让图片能落盘，核心补了两个对称面

`fs.read` 早就有 `encoding:"base64"` 了，这一轮把**写**和**下载**也补齐：

| 能力 | 新参数 | 用途 |
|---|---|---|
| `core.fs.write` | `encoding:"base64"` | 写二进制（存生成的图片），否则无效字节被替换、图就废了 |
| `core.net.request` | `encoding:"base64"` | 响应体按 base64 返回（接口只给 URL 时要把图下载回来） |

两处都只**新增可选参数**，不给就是原来的行为。

### 文字和识图可以分开配

便宜快的纯文本模型不会看图，视觉模型又通常更贵。所以 llm 插件支持给"带图片的请求"单独一套配置 —— 日常聊天用便宜的，只有真发了图才切过去：

```jsonc
// /tool llm.v1 configure
{"visionModel": "deepseek-flash"}                      // 只换模型（同一个服务商）
{"visionProvider": "ollama", "visionModel": "qwen2-vl"} // 连服务商一起换（地址自动跟着换）
{"visionModel": ""}                                     // 清空 = 回到"跟主配置一样"
```

留空的项一律**回落到主配置**，所以不配也完全不影响现有行为。

### AI 发图：消息里的路径会自动画成缩略图

模型只要在回答里写出**绝对路径**（它本来就会说"已保存到 `E:\AI资料\cat.png`"），界面就会在那条消息下面画出来：

```
  ▣ cat.png              [ 打开 ]
  E:\AI资料\cat.png
  ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀   ← 真彩色缩略图（半块字符拼的）
```

- **不需要模型配合任何特殊格式** —— 认的是路径，不是协议
- **不需要终端支持图形协议**：用 `▀` 上下各拼一个像素（前景=上、背景=下），普通终端就能显示
- 点「打开」或路径本身 → 交给系统默认看图程序（走 `core.shell.open`）
- 只认绝对路径；`sub/dir/a.jpg` 这种相对路径不会被误认
- 图片走缓存，同一张图只读一次

### 识图能力取决于模型（DeepSeek V4.1 实测可用）

图片按多模态（`image_url` + base64）发出，能不能"看懂"取决于模型：

| 模型 | 识图 |
|---|---|
| `deepseek-flash`（V4.1 Flash） | ✅ 原生多模态，实测能读出截图里的联系人名、未读角标、通话状态 |
| `deepseek-chat` / `deepseek-reasoner`（旧版） | ❌ 纯文本 |
| Qwen-VL / GLM-4V / gpt-4o | ✅ |
| 本地 Ollama（llava / qwen2-vl 等） | ✅（走 `images` 字段） |

不确定时看 `logs/com.official.llm.log`：

```jsonc
[INFO] 本次请求含 1 张图片 {"provider":"openai","model":"deepseek-flash", …}
```

**有这行 = 图发出去了。** 模型若回"我看不了图"，先看这一行在不在：在，是模型不支持（换模型）；不在，才是链路的问题。

### 两个踩过的坑（改起来都只有几行，但很难查）

**一、别在正文里写图片的文件名。** 早先正文带了 `[图片附件: cat.png]`，模型于是把它当成"工作区里的一个文件"，掉头去 `list_dir` 找它 —— 明明图就贴在这条消息里。现在图片**只放 part、不写正文**；历史记录里的附件名由 `conversation`/`session` 那层单独标注（那是给人看的，不给模型）。

**二、思考模型会给出空的 `content`。** DeepSeek V4.1 默认开启 thinking，思考放在 `reasoning_content` 里。思考用光 token 预算时 `content` 是空的 —— 原样返回，用户看到的就是一条"没反应"的空消息。现在会明确报出来（提示调大 `maxTokens`，或把思考摘录带出来），不再让界面出现空白。

---

## 用量监控（usage-monitor）

`com.official.usage-monitor` 提供 `usage.v1`，回答三个问题：**账户里还剩多少钱**、**这台机器一共烧掉多少 token**、**缓存命中率是多少**。

浮层长在终端右上角（第 2 行起，第 1 行留给工作台的标题栏），默认每秒重画一次：

```
┌─ DeepSeek 用量 ──────────────────┐
│ 余额                      ¥12.34 │
│ 输入               1,234,567 tok │
│ 输出                  89,012 tok │
│ 缓存             72.9% (900,000) │
│ 请求           128 次 · 18:04:05 │
└──────────────────────────────────┘
```

```
你 › /tool usage.v1 snapshot                        # 余额 + 累计 + 命中率，一次看全
你 › /tool usage.v1 balance                         # 只查余额（GET /user/balance）
你 › /tool usage.v1 reset                           # 累计清零（余额保留）
你 › /tool usage.v1 configure {"refreshSeconds":30} # 余额轮询间隔（默认 60 秒）
你 › /tool usage.v1 configure {"overlay":false}     # 关掉浮层，其它照常
你 › /tool usage.v1 configure {"apiKey":"sk-…"}     # 单独给它一把 key；传 "-" 清空
```

**密钥不用重填。** 顺序是：本插件自己的配置 → `llm` 插件的配置（你早就填好了的那把）。后者经能力代理读 `plugin-configs/com.official.llm.json`，所以 `usage-monitor` 声明了 `filesystem:read`，但**只读这一个文件**。

### 命中率为什么必须由 llm 报

缓存命中率 = 命中的输入 token / 全部输入 token，而 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` 只出现在 DeepSeek 的**响应体**里。默认解析只留下三个总量字段，命中信息在解析那一步就丢了 —— 等 `chat()` 返回到 agent / 界面时，已经没有任何地方能把它捞回来。

所以 `plugins/llm/usage_bridge.go` 把 openai 这个 provider 换成了一个"同一件事、顺手报数"的实现：发一样的请求，解析完整的 usage，再 emit 一条 `llm.usage`，由用量插件订阅并累计。想回到原实现：

```
你 › /tool llm.v1 configure {"provider":"openai-legacy"}
```

**桥的代价是零**：没有订阅者时事件被核心直接丢弃，`emit` 失败也不影响模型调用本身。

### 统计落在哪

累计写进 `plugin-configs/com.official.usage.json`，**重启不清零**（`reset` 才清）。最多丢最后 5 秒的计数 —— 落盘是后台每 5 秒一次，而退出时刻意不再发 RPC：核心收 `core.shutdown` 时已不处理请求，在这里 `SetConfig` 会把退出拖到超时。

浮层**不抢 `ui.v1`**：它自己打开控制终端（`CONOUT$` / `/dev/tty`）画那一小块。代价是工作台每帧全屏重绘会把它盖掉，所以按固定节奏补画；窗口窄到画不下时干脆不画。

---

## 看 AI 在干什么（过程框）

一次回答要跑好几轮：模型想一会儿、调工具、看完结果接着想。原先这段时间里，消息区只有一个转圈的占位框 —— AI 想了什么、调了什么、卡在哪一步全都看不见，能得到的唯一反馈是"它在转圈"。

现在那里多一个「AI 在做什么」的框，跟着事件实时更新（真机抓帧）：

```
┌─ AI 在做什么 · deepseek-flash ───────────────────────┐
│ 第 3 轮 · 完成 · 3 次工具调用 · 用时 6.0s            │
│ ✓ read_file · path=plugins/usage-monitor/manif… 1ms  │
│ ✓ list_dir · path=plugins                       1ms  │
│ ✓ read_file · path=plugins/fs-tool/manifest.js… 1ms  │
│ 思考：Now answer. Also note the correction…           │
└──────────────────────────────────────────────────────┘
```

三件事都看得见：**在干什么**（第几轮、此刻在思考还是调哪个工具、已用多久）、**思考什么**（模型给的 reasoning）、**调了什么工具**（工具名 + 参数摘要 + 耗时，成功 ✓ 失败 ✗）。

**它从事件来，不从调用来。** agent 把过程抛到总线（`agent.think` / `chat.tool.before` / `chat.tool.after` / `agent.done`），界面只订阅、不调用谁 —— 换掉编排插件，画面照样在；反过来，没装 agent（或直接用 `llm.v1`）时退回原来的转圈框，不会空着。

### 思考内容为什么原先看不到

它**一直都在响应体里**（`reasoning_content`），但原先只在"回答为空"时被拿来解释原因，调用方拿不到 —— 于是界面上的 AI 只会转圈。现在 llm 把它随 `chat` 返回一起带出去（新增 `reasoning` 字段，老调用方照旧不读），agent 截到 300 字后随事件广播。

裁这一刀不是为了省事：事件是**广播**的，几千字的思考原样丢上总线既浪费，界面那两三行也摆不下 —— 要的是"它现在在想什么"，不是全文。

### 两个细节

- **生成结束后框不立刻消失**：留在那儿还能回看"它刚才调了什么、哪一步失败了"，下一次发消息才换新的（翻版本、切会话不会冲掉它）。
- **工具参数只显示前两个键**：原始 JSON 是给模型看的，一个 `read_file` 的参数连带转义能有几千字符，铺在界面上只会把正文挤没。取 `path=… · encoding=…` 就够认出它在读哪个文件。

---

## 用量浮窗：能拖、能收、不再挡着

右上角那块小窗原来是自己往终端上写的。它不知道工作台画了什么，工作台也不知道它在哪儿 —— 于是工作台每重绘一帧就把它盖掉，插件只能每秒补画一次（肉眼看得见的闪）；更谈不上拖动，因为**鼠标在工作台手里**。

现在反过来：**插件只广播数据，画面归终端的所有者画**。

- **拖**：按住浮窗拖到哪儿停在哪儿，位置会记住（写在插件配置里，下次启动还在那儿）
- **收**：在上面轻点一下收成一条 `用量 ¥18.98 · 77.4%`，再点一下展开
- **挪**：`Alt+方向键` 一格一格挪 —— 手比鼠标稳的时候用它
- 拖出屏幕也不会消失：位置始终夹在可见范围内

数据走浮窗协议：usage-monitor 提供 `ui.widget.usage.v1`（`list`/`render`），界面发现后问清单、拉内容，自己画 + 拖 + 收起；数据一变 usage-monitor 就发 `ui.widget.changed`，界面据此重新拉一帧。没有工作台时（比如用极简界面）它照旧自己画 —— 功能不丢，只是不能拖。

> 实现上有个绕不开的坎：浮窗是**贴**在已经排好的行上的，而行里混着 ANSI 样式码。按字节下标切会把一个 `\033[38;5;111m` 砍成两半，屏幕上立刻是一串乱码。所以按**显示列**切（`spliceColumns`），并且：被盖住那一段右边的内容要接回原来的颜色，否则一路被浮窗的颜色染到行尾；起点正好切在一个汉字身上时，那个字整块让位、让掉的半格补成空格 —— 不补的话后面的内容会整体左移一列，界面从那儿开始歪。
>
> 宽度也不能拿含样式码的字符串去量：`\033[38;5;109m` 不占显示列，量出来会比实际宽十几个列，浮窗就贴错位、还把右边一行的内容吃掉一截。所以宽度由形态常量给（展开 30 列 / 收起 26 列），量字符串这条路只在关掉颜色时才碰巧对得上 —— 真终端一定开着颜色。
>
> 还有一处更隐蔽：**光有代码收得到拖动，终端不肯发也是白搭**。终端的鼠标上报是分档的，只开"点击"那一档时，拖动过程一个字节都不发，界面永远不知道鼠标挪到哪儿了，浮窗就钉在原地。所以进入界面时同时开"按住移动也上报"那一档；不支持的老终端会自动退回只能点，这时用 `Alt+方向键` 挪。Windows 上另有一条原生通路，控制台"按住左键移动"的记录以前直接被丢掉了（旧注释写着"界面不需要"）—— 现在一并接上。

---

## 写一个插件

四套 SDK 共用同一份协议：**Go / Python / Java / C**。完整对照表与各语言快速开始见 [`sdk/README.md`](sdk/README.md)。

一句话概括：注册服务 → 处理 `core.invoke` → 出站调用要等响应。区别只在语法和线程模型的实现方式。

下面是 Go 版示例（其余语言见 SDK 文档）：

```go
package main

import (
	"encoding/json"
	"aiwork/sdk/go/plugin"
)

type Echo struct{ ctx *plugin.Context }

func (e *Echo) Activate(ctx *plugin.Context) error {
	e.ctx = ctx
	ctx.On("chat.message", func(payload json.RawMessage) {
		ctx.Log("info", "收到 chat.message", nil)
	})
	return ctx.Provide("echo.v1", map[string]any{"description": "回显输入"})
}

func (e *Echo) Handle(service, method string, args json.RawMessage) (any, error) {
	if method != "run" {
		return nil, plugin.Errorf(plugin.CodeMethodNotFound, "echo.v1 不支持 %s", method)
	}
	var in struct{ Text string `json:"text"` }
	_ = json.Unmarshal(args, &in)
	return map[string]any{"echo": in.Text}, nil
}

func (e *Echo) Deactivate() error { return nil }

func main() { plugin.Serve(&Echo{}) }
```

SDK 提供的 `Context` 方法与 6 个接口一一对应：
`Provide` / `Consume` / `Invoke` / `Emit` / `On` / `GetConfig` / `SetConfig` / `Log` / `SandboxRun` / `GetSecret` / `SetSecret`。

> 注意：插件进程的 **stdout 只能输出 JSON-RPC**。日志请走 `ctx.Log` 或 `plugin.Logf`（写 stderr）。

Python / TypeScript 插件走语言适配器路线（规范 2.7）：适配器插件把 `ctx.provide` 翻译成 `core.provide`，核心完全不感知 Python 的存在。

---

## 引导菜单

格式化后进入；`plugins/` 为空时也会自动进入。

```
[系统] 格式化完成，系统已重置。
[系统] 请输入数字选择操作：
  1) 安装官方极简 UI（推荐）
  2) 从网址安装 UI 插件
  3) 从本地文件夹安装 UI 插件
  4) 跳过，稍后手动安装
>
```

命令：`help` / `install <url|path>` / `install builtin` / `list` / `exit`。

---

## 兼容性宪法

**公开契约只增不减。**

| 类型 | 能否删 | 说明 |
|---|---|---|
| 公开 API 方法 | ❌ | 删了插件就崩 |
| 公开事件名 | ❌ | 监听者会失效 |
| 公开配置键 | ❌ | 插件配置会丢 |
| 公开对象字段 | ❌ | 反序列化会坏 |
| 枚举值 | ❌ | 只能新增 |
| 内部实现 | ✅ | 不影响对外行为 |

弃用三阶段：标记 `deprecated` → 新旧并存 → 至少一个 minor 周期后在大版本移除。

本实现相对规范**新增**（未删减任何内容）的公开契约：

- 核心转发 `core.invoke` 时的参数扩展：`caller`、`forwarded`
- 新增公开事件 `plugin.state`（载荷 `{pluginId, state}`）
- `core.log` 同时作为事件广播，日志插件可订阅接管
- 新增能力方法 `core.fs.delete` / `core.fs.move` / `core.fs.mkdir` 与权限 `filesystem:delete`
- `core.fs.read` / `core.fs.write` / `core.net.request` 各新增可选参数 `encoding`
  （`"base64"` 时二进制安全：读图片、写图片、下载图片都靠它），返回体相应多一个 `encoding` 字段
- 新增能力方法 `core.shell.open` 与权限 `shell:open` —— 只用系统默认程序打开一个已存在的文件，
  命令与参数由核心写死，插件只能给路径（比 `process:spawn` 窄得多）
- 派生插件 `com.official.imagegen`（`imagegen.v1`）与 llm 的识图配置项
  `visionProvider` / `visionBaseUrl` / `visionApiKey` / `visionModel`
- 消息格式新增可选字段 `parts`（多模态：文本 + 图片），`agent.v1` / `llm.v1` / `session.v1` /
  `conversation.v1` 均支持 `attachments` 入参；没有这些字段时行为与从前完全一致
- `pending-restart.json` 的变更记录新增 `by` 字段（发起方插件 ID）与 `enable` / `disable` 动作，
  核心启动时据此校验权限并执行
- 新增公开事件 `llm.usage`（llm 插件每次模型调用后 emit，用量插件订阅累计）
- 新增 provider 名 `openai-legacy`（与 `openai` 同一套接口，但不发 `llm.usage`）
- `llm.v1.chat` 的返回新增 `reasoning` 字段（思考模型的思考过程）
- 新增公开事件 `agent.think`（`phase` 为 `start` / `done` / `error`，带 `thinking` / `text` / `toolCalls`）
- 新增公开事件 `ui.widget.changed`（浮窗内容变了，界面据此重新拉一帧）
- 新增公开事件 `ui.overlay.claim`（界面认领终端上的浮窗；插件收到后停止自绘，改为走 ui.widget.* 浮窗协议）

---

## 核心规模

规范预估约 1600 行，规范同时规定“核心代码不超过 2000 行”。本实现的实际规模：

| 包 | 规范预估 | 实际 |
|---|---:|---:|
| `core`（入口 + 参数解析） | 100 | 90 |
| `loader`（扫描 / manifest / 版本 / 拓扑排序 / 解压 / pending-restart / 运行时解析） | 350 | 801 |
| `process`（子进程管理 + JSON-RPC 收发） | 300 | 484 |
| `jsonrpc`（消息模型，规范未单列） | — | 112 |
| `host`（粘合层 + 6 接口 + 生命周期，规范未单列） | 150 | 850 |
| `registry`（服务注册表 + 事件总线 + 命名校验） | 200 | 193 |
| `capability`（能力代理，规范 3.5 要求“第一阶段做基础版”） | — | 494 |
| `config`（配置持久化） | 100 | 103 |
| `format`（格式化 + 信号 + 控制通道） | 150 | 191 |
| `bootstrap`（引导菜单 TUI） | 200 | 266 |
| `logs`（日志） | 50 | 110 |
| **合计** | **约 1600** | **3694（其中有效代码 3012，其余为注释与空行）** |

另有 574 行规范条款测试（`*_test.go`），不参与运行期，不计入上表。

SDK 与插件都不属于核心，不计入上面的约束：

| 部分 | 行数 |
|---|---:|
| `sdk/go/plugin/` | 611 |
| `sdk/python/` | 484 |
| `sdk/java/` | 996 |
| `sdk/c/`（含手写 JSON DOM） | 1433 |
| `plugins/minimal-ui/`（含 369 行测试） | 2102 |
| `plugins/workbench/`（含 793 行测试） | 3950 |
| AI 四件套 `llm` + `tool-executor` + `agent` + `session`（非空行数） | 2486 |
| `plugins/fs-tool/`（非空行数） | 110 |

超出预估的部分全部用于**把预估变成能跑的代码**，职责未越界：

- `host` 是规范中隐含但未单列的粘合层（规范把它摊在“错误处理 + 工具 150 行”与各模块里）
- `loader/archive.go` 的 zip 解压、防 zip slip、剥单层根目录、非 Windows 补齐可执行位
- `capability/` 是规范 3.5 要求的“能力代理基础版”，规范未给行数预算
- 跨平台分支（Windows 无 `SIGUSR2` / 无 `syscall.Exec` / 文件锁 → `pending-delete/`）
- 每一个 RPC 分支的完整错误处理，而不是 happy path

若要严格收敛到 2000 行，可裁剪顺序为：注释密度 → 引导菜单的安装分支合并 → `logs`/`config` 的空行与辅助方法。**不建议**通过裁剪错误处理或能力代理来达标。

---

## 规范符合性

核心**强制校验**的规范条款（违反即拒绝，不是建议）：

| 条款 | 校验点 | 失败表现 |
|---|---|---|
| 2.2 | manifest 必填字段、`id` 反向域名、`version` 语义化 | 插件被跳过并告警 |
| 3.3 | `provides` 服务名必须带版本后缀 `.vN` | `core.provide` 返回 `-32602` |
| 3.3 | `compat.core` 版本范围 | 不兼容直接拒绝加载（`-32005`） |
| 3.7 | 事件名必须带命名空间 | `core.on` 返回 `-32602`；`core.emit` 记警告不丢包 |
| 3.7 | 权限名小写 + 冒号分隔 | manifest 校验失败 |
| 3.4 | 未知权限只记录告警，不拒绝 | 写入 `logs/core.log` |
| 2.6 | 安装后写 `pending-restart.json`，启动时消费并清除；**删除插件先 `core.unload` 热卸载**（卸载比加载安全，是"绝不热更新"的唯一例外） | 装插件重启生效；删插件当场卸载 |
| 3.5 | 能力调用按 manifest 权限闸门放行，未声明即 `-32002` | 默认拒绝 |
| 3.5 | `fsRoot` 非空时路径必须落在根内（含符号链接解析） | 越界 `-32002` |

`go test ./core/...` 可执行验证上述条款（`loader/rules_test.go`、`registry/rules_test.go`、`capability/proxy_test.go`）。

已知未实现项（规范要求但当前版本没有）：

- **3.6 插件包签名验证**：规范标为可选，未做。
- **2.5 `degraded` 状态**：状态机已定义并保留，MVP 中没有任何路径会进入该状态。
- **3.5 的强制力**：能力代理是协作式的。插件绕过代理直接调用系统 API 无法被阻止，需等第二阶段的沙箱插件。
- **3.8 核心不超过 2000 行**：当前 3012 行有效代码，**不符合**。见上一节的裁剪顺序。

## 已验证的链路

以下场景均在 Windows + Go 1.27 上实际跑通：

- [x] 启动 → 扫描 → manifest 校验 → 拓扑排序 → 子进程启动 → `core.init` → 服务注册 → `active`
- [x] `plugins/*.zip` 自动解压为 `*.plugin/`
- [x] 服务发现：`session.v1` / `agent.v1` / `llm.v1` / `toolExecutor.v1` 未安装时返回 `-32000`，已安装的返回 `pluginId`
- [x] **AI 全链路**：界面输入 → `session.v1` → `agent.v1` → `llm.v1` / `toolExecutor.v1` → `readFile.v1` → 能力代理 → 文件内容原路返回（12 项断言全过）
- [x] **附件 · 二进制保真**：`core.fs.read(encoding=base64)` 读 11 MB 的 `core.exe`，解出的字节与原文一致（默认 UTF-8 读取会把无效字节换成 U+FFFD）
- [x] **附件 · 全链路**：图片附件经 `conversation.v1 → agent.v1 → llm.v1` 送达，日志记下 `本次请求含 1 张图片`
- [x] **文本画图（真机）**：`svgdraw.v1 draw` → `deepseek-flash` 写出 SVG → 落盘 364 字节 → 回读验证 `<svg xmlns=… viewBox="0 0 600 400">` 结构完整；全程**不依赖任何外部图像服务**
- [x] **改后缀不行（实验）**：把文本改名成 `.png`，解码报 `image: unknown format`，前 8 字节与 PNG 魔数不符
- [x] **生图插件独立上线**：`imagegen.v1` 单独一个插件（不融进 llm），核心加载后 `12 个插件运行中，0 个被跳过`；没配密钥时给出可操作的提示而不是静默失败
- [x] **写/下载二进制的对称面**：`core.fs.write` 与 `core.net.request` 各加可选 `encoding:"base64"`，写图片与下载图片都不会损坏
- [x] **发图 · 缩略图**：消息里的绝对图片路径会被认出并画成真彩色缩略图（半块字符），同一张图只读一次；无颜色终端下不画（彩色块会成乱码）
- [x] **发图 · 打开**：点「打开」经 `core.shell.open` 交给系统默认看图程序；能力测试覆盖了权限隔离、目录拒绝、围栏、以及"路径作为独立 argv 传入"（不拼 shell）
- [x] **文字/识图分开配**：配了 `visionModel` 后，只有带图片的请求切过去；留空项一律回落主配置，不配也完全不影响现有行为
- [x] **识图 · 真机**：`deepseek-flash` 对程序绘制的测试图（黄底 + 左上黑方块 + 右下白圆）准确报出底色、两个形状与各自位置；对真实 Windows 微信截图读出联系人名、6 条未接通话、28 条未读角标
- [x] **识图 · 不误导模型**：图片附件不再往正文写文件名后，模型不再去 `list_dir` 找那张图
- [x] **空回复不再静默**：思考占满 token 预算时明确报错并提示调大 `maxTokens`，不会出现一条空白消息
- [x] **附件 · 不进历史**：发一张图后会话文件仍只有 3 KB，节点里只留 `[附件: cat.png]`，不含任何 base64
- [x] **附件 · 只发图不写字**：输入框空着也能发出去，自动补一句占位正文
- [x] **附件 · 拖拽识别**：`C:\pics\cat.png` 回车后变成附件（普通提问如「C:\ 是网盘吗」不会被误吞）
- [x] **附件 · 认错有退路**：路径读不出来时内容留在输入框，再按一次回车就当文本发出去
- [x] **附件 · 大文件拒绝**：超过 3 MB 直接报错，绝不把截断的半个文件发给模型
- [x] **工具循环**：模型要求调工具 → 执行 → 结果回喂 → 二次推理给出最终回答
- [x] **工具错误回喂**：`read_file` 缺必填 `path` 时，错误作为工具结果交回模型，整轮对话不中断
- [x] **会话持久化**：历史写入 `plugin-configs/com.official.session.json`，重启后仍在
- [x] **逐级降级**：缺 `toolExecutor.v1` 时 `agent` 自动退化为纯聊天，并在响应里说明原因
- [x] **运行时注册工具**：`toolExecutor.v1.register` 后立即可执行，并持久化到插件配置
- [x] **零配置可跑**：无密钥时 `llm.v1` 自动选离线 mock provider，整条链路可复现
- [x] 事件总线：`chat.message` / `chat.response` / `core.log` / `plugin.state` 可在界面内实时显示（`/events on`）
- [x] **事件订阅真的订阅了**（用户报过的 bug）：`ctx.On` 曾经只注册了本地处理函数、没向核心发 `core.on`，于是**所有插件的 `ctx.On` 都是死的** —— 界面里那套「插件状态变化就自动刷新」因此整条失效，且不报任何错。现在 SDK 会真的订阅，并有回归用例直接断言「插件究竟有没有把 `core.on` 发出去」
- [x] **界面快照自动追平**（用户报过的 bug）：`workspace` 排在 `workbench` 之后激活，界面启动那一刻必然扫不到它，过去要手点一次「重新扫描」才对得上；现在收到 `plugin.state` 后防抖补扫一次，实测**零输入**下第 8 帧自动变成 ✓
- [x] 终端 UI 不污染硬通道：真实控制台（CONIN$/CONOUT$）下运行后，核心日志中无任何 JSON-RPC 解析错误
- [x] 全屏 TUI：真实控制台下成功进入备用屏 + 原始模式（日志 `极简 UI（全屏终端）已就绪`）
- [x] 界面降级：终端不可交互时自动退化为行式界面，功能完整（探测 / 聊天 / `/tool` / 退出）
- [x] 排版不变量：8 种终端尺寸 × 有无颜色共 16 组，每行显示宽度精确等于终端宽度
- [x] 插件崩溃：核心存活，`readFile.v1` 立即置为不可用，`ui.v1` 不受影响，服务自动注销
- [x] 格式化（HTTP + Token）：`plugins/` 与 `plugin-configs/` 清空，`core.json` 与 `logs/` 保留，1 秒内完成重启
- [x] 错误 Token 被拒绝（403）
- [x] 引导菜单：从内置 `runtime/builtin/minimal-ui.zip` 安装极简 UI，装完即可用
- [x] 依赖缺失 → 跳过并警告；循环依赖 → 跳过并警告；`apiVersion` / `compat.core` 不兼容 → 拒绝加载
- [x] 能力代理放行：`fs-tool` 声明 `filesystem:read` 后，`readFile.v1` 经核心读取文件成功，审计日志留有记录
- [x] 能力代理默认拒绝：把 `fs-tool` 的 `permissions` 改成 `[]`，同一调用返回 `-32002 权限不足`（真机验证）
- [x] 命名规范强制：服务名不带版本、事件名不带命名空间都被拒绝注册
- [x] pending-restart：引导菜单安装后写标记，下次启动消费并写入日志后清除
- [x] **工作台 UI 的鼠标链路**：注入 SGR 鼠标序列驱动真实运行，点击标签切页、点击列表选中插件、滚轮滚动全部生效（抓取真实帧核对）
- [x] **回车换行、点按钮发送**（用户要求的行为变更）：输入框支持多行（最多 6 行，超出跟着光标滚动），回车只插入换行，发送走「发送」按钮或 Ctrl+Enter；消息里的换行原样保留。真机抓帧验证：喂两行输入 → 点「发送」→ 消息框里是两行、输入框清空并缩回一行
- [x] **长文本压不住「发送」**：折行宽度预先扣掉按钮那几列，70 列窗口 + 远超一行的输入下按钮仍可点（用例断言那一列的热区属于按钮，并真的点它发出消息）
- [x] **角色扮演 · 记忆真的进了对话**：往角色记忆里放一个暗号 → 在聊天页问它暗号 → 模型回 `ZEBRA-7`（暗号只存在于记忆里，真机验证）。启用时推送的系统提示词在 `plugin-configs/com.official.agent.json` 里直接可见
- [x] **角色页**：角色列表 + 8 个可编辑字段 + 新建/保存/启用/删除（二次确认）；记忆栏显示末尾，多行记忆不会把版面劈开（用例断言帧高精确等于窗口高）
- [x] **remember 工具重试**：`tool-executor` 的目录序在 `roleplay` 之后，一次注册必然失败 —— 加 250ms 重试后实测注册成功（日志可见）
- [x] **本机服务的 401 会自己说清楚**（用户报过的 bug）：LM Studio 的「要求 API 令牌」开关被打开后，密钥框里的云端密钥必然每个请求都被拒（实测连回 4 次 401）。现在 **401/403 + 本机/内网地址**会附带「开关在哪、令牌在 Manage Tokens 里建且只显示一次、密钥框是全平台共用的」，**云端地址则保持沉默**（那种情况问题确实在密钥本身）—— 真机复现验证，提示原文出现在对话区与底栏
- [x] **工作台 UI 的真实控制台链路**：`CONIN$`/`CONOUT$` 打开、关闭快速编辑模式、开启鼠标输入、原生事件读取循环全部初始化成功
- [x] **插件清单自动发现**：界面经能力代理读 `plugins/*/manifest.json`，所有已安装插件全部列出并标出运行状态
- [x] **设置页写配置**：填入 API Key → `llm.v1.configure` → 立即生效，界面与日志只回显打码值
- [x] **设置页重写为「云端 / 本地」两条分支**：云端＝知名平台快捷连接 + 自定义 / 冷门平台连接；本地＝系统扫描本机 + 已知本地应用 + 自定义本地服务。两栏内容严格分开，云端那一栏不出现本机应用（用例断言），位置本身就是分组暗示
- [x] **设置页三步式**：点服务预设 → 地址与模型自动填好 → 粘 API Key → 保存；焦点自动落在密钥框，不用先点输入框
- [x] **本机扫描 · 扫不到也有出路**：扫描结果逐条给出协议、模型数与「连接」按钮（地址 / 端口 / 耗时单独一行），一台都没有时明说「还没扫到本机 AI 服务」并把用户引向「自定义本地服务」的**手动输入**（用例断言这两句都在画面上）
- [x] **多协议模式**：右栏「端点与协议」三个选项（OpenAI 兼容 / Ollama 原生 / 离线 mock）都能点、点完 provider 真的换过去 —— 协议选错是「连不上」的头号原因，且状态码拦不住
- [x] **多应用格式**：「已知本地应用」列出 Ollama / LM Studio / vLLM / llama.cpp / text-generation-webui / KoboldCpp / Jan / GPT4All / SGLang / Xinference / 离线 mock，并把**默认端口**摊成图例（`Ollama:11434 · LM Studio:1234 · vLLM:8000 …`）；图例按条目打包换行，名字与端口不会被拆到两行（用例断言）
- [x] **窄窗口也能存**：窗口窄下来时这一页并成一栏竖排、整页自成一屏可滚 —— 否则右栏（API Key 与「保存并生效」）永远在可视区之外，配置根本存不下去（用例断言：80 列下滚到底后保存按钮够得着，且滚动位置不越界）
- [x] **粘贴不弹页**（用户报过的 bug）：注入含制表符与换行的括号粘贴，密钥完整落进输入框、页面纹丝不动（真机验证）
- [x] **保存看得见**（用户报过的 bug）：保存后输入框换成打码确认、状态栏说明「已写入」；保存失败密钥留在框里；后台回读不覆盖正在输入的内容（真机验证四项断言）
- [x] **模型列表来自服务端**：`llm.v1.models` 支持 OpenAI 兼容的 `GET /models` 与 Ollama 的 `/api/tags`，不在客户端硬编码模型名
- [x] **两个 UI 共存**：都提供 `ui.v1`，抢不到的那个安静退出，不与另一个抢终端
- [x] **界面排版不变量**：5 种尺寸 × 3 个页面逐行校验宽度，且无颜色渲染下不出现任何转义序列
- [x] **删除未运行的插件**：目录 + 配置一次删净，重启后无残留
- [x] **删除正在运行的插件**：当场删不掉时**原样保留**（不产生半删目录），记为 deferred，重启前加载阶段清干净
- [x] **启动清理**：核心在加载任何插件之前消费 `pending-restart.json`，目标目录 / 配置 / 日志各清一次并有审计日志
- [x] **越权删除被拦**：发起方未声明 `filesystem:delete` 时，启动时忽略该记录并告警
- [x] **删除自检**：`warnings` 如实列出消费方、依赖方与 `ui.v1` 提醒；删自己返回 `-32602`
- [x] **路径穿越被拒**：`pending-restart.json` 里带 `..` 或分隔符的目录名 / 插件 ID 一律忽略
- [x] **界面可点启停与删除**：插件详情里的「启用/禁用」「删除插件」按钮，删除需二次确认，换选中或 `Esc` 会撤掉确认
- [x] **启停往返**：禁用 → 重启后该插件不再加载 → 启用 → 重启后恢复加载
- [x] **运行中插件的启停被推迟**：目录被占用改不动名时记为 pending，启动时执行，不产生半改状态
- [x] **工作区文件操作**：指定文件夹后创建 / 写入 / 追加 / 读取 / 复制 / 移动 / 删除 / 建目录 / 列目录 / 目录树全部可用
- [x] **全盘可访问**：相对路径落工作目录，绝对路径（含他盘）直通；核心运行时目录本身与其一级子目录仍不可删，工作目录自身不可删
- [x] **「工作区」页由插件自带**：workspace 插件提供 `ui.page.workspace.v1` 并自己渲染这一页；单测覆盖"每行恰好等于给定宽度""点目录进入""回车设为工作区""点击定位光标""状态变了必须喊一声"
- [x] **「角色」页搬进了 roleplay 插件**：界面那边删掉 `tabRole`、三个角色类型、一屏渲染函数与 20 多个后端方法；插件侧单测覆盖"每行不超过给定宽度""点列表换人""打字进当前字段""Ctrl+Enter 保存""没动过的记忆不提交""删除要两次""预览显示真正会发出去的那段""焦点跑出可视区时自己滚过去""模型 remember 之后页面跟着刷新"
- [x] **「插件」页搬进了 plugin-manager**：界面那边删掉 `tabPlugins`、一屏渲染、`selectPlugin`/`toggleSelected`/`deleteSelected` 与两个转发方法；插件侧单测覆盖"每行不超过给定宽度""宽屏分两栏窄屏一列""读不出 manifest 的目录不给启停删除入口且业务层再挡一道""插件管理器自己不可停删""启停真的改目录名并通知重画""删除第一次只进确认态且把目录/配置/日志与依赖者摊开""Esc 撤销确认""重新扫描能看到新放进去的插件""焦点跑出可视区时自己滚过去"
- [x] **插件自带界面的通用机制**：界面按 manifest 里的 `ui.page.*` 发现页面 → 合成到标签栏 → 转发点击与按键；插件画歪了（行宽不对）界面自己剥样式重排、渲染出错只试一次
- [x] **插件没了，页面就没了**：订阅 `plugin.deleted` 触发重扫；判据是"带页面的那组服务变过"（不是"清单为空"）；帧缓存、在途请求、屏幕热区一起作废；正看着那一页时自动退回聊天。**删除是重启生效的、目录此刻还在**，所以收到 `plugin.deleted` 还要立刻摘掉它的页面、把"已删"记进 `droppedPlugins` 让后续每轮扫描都滤掉它 —— 否则删完标签还在（用户报过的真实 bug）。用例覆盖删除、崩溃、目录仍在、与"清单没变时不重复询问"
- [x] **`@ui:` 宿主口子**：插件想让界面做事时用（`@ui:chat` 把文字放进输入框、`@ui:restart` 请核心重启）—— 这两个都只有界面自己做得了
- [x] **界面与插件启动竞态**：插件是逐个启动的，界面可能先起来 —— 页面清单靠"每次拿到服务快照就补问一次"接上，不会把空清单摆给用户
- [x] **SDK：单选原语 `Block.Chip`**：插件页里"平台 / 协议 / 模式"这类多选一项，现在有统一画法（选中带 ● 与高亮），不用每个插件各自用 `Text` 拼一个
- [x] **「模型」页搬进了 llm 插件**：`presets.go` 那张 20 条服务目录（地址 / 默认模型 / 密钥申请地址 / 本机端口）连同 `wireLabel`/`portOf` 一起搬进 llm；插件侧单测覆盖"每行不超过给定宽度""云端清单里不混本机应用""点平台填地址与默认模型并把焦点送到密钥框""协议跟着端点走""扫描结果回填""打字只进聚焦的框""没改动点保存会说一声""焦点跑出可视区自己滚过去""目录与扫描候选表不飘开"
- [x] **「预算」页搬进了 agent 插件**，并删掉了界面里的「设置」页：删掉 `settings.go` / `presets.go` / `settings_test.go`、`tabSettings`、设置页那一整套状态字段、动作与转发方法（`loadLLM`/`saveLLM`/`testLLM`/`listModels`/`discoverLocal`/`loadAgentBudget`/`saveAgentBudget`）。界面现在只剩「聊天」这一页
- [ ] **插件自带界面的真机验证**：真实终端里的鼠标点击、滚轮翻页、键盘输入手感还没跑过 —— 单测全绿不等于手感对
- [x] **清掉界面里 formField 那一套死代码**：`formField` 类型、`defaultFields`、`fieldRow` 与一整套字段渲染/光标/拖选辅助、`cycleFocus`/`settingsFocusKnown`/`focusOrder`/`typeTarget`/`fieldValue`，以及编辑函数里所有"落进字段"的分支。聊天输入框的拖选与复制**原样保留**（它们走的是 `st.input` / `st.selOn` 自己那套，跟 formField 无关）
- [x] **插件页面能拖选 + 复制了**：SDK `Field` 补上选择（`SelStart`/`SelExtend`/`Selection`，选中段画 `SSel` 底色、编辑即作废）；协议加 `sel` 动作（宿主把拖到哪一列转过去）与 `WantCopy` 声明；`Ctrl+C` 专职复制（有选择就回 `copy` 让宿主写剪贴板，没选择就回空），退出独立成 `Ctrl+Q`，不再和复制挤一个键。五个带输入框的页面（模型 / 预算 / 文生图 / 工作区 / 角色）都接上了；插件页是无输入框的清单，用不着
- [x] **「文生图」配置页搬进了 imagegen**：之前文生图用什么服务、密钥、模型、尺寸只能靠环境变量改，界面上没入口。插件侧单测覆盖"每行不超过给定宽度""点服务商填地址与默认模型并把焦点送到密钥框""打字只进聚焦的框""没改动点保存会说一声""预设目录与 defaultModel/defaultBaseURL 不飘开"
- [x] **通用浮窗协议 `ui.widget.*`**：浮窗从 usage-monitor 专用变成任意插件可用 —— 插件声明 `ui.widget.<名字>.v1` 并实现 `list`/`render`（内容自己渲染），界面负责发现、画、拖、收起、记住位置；数据变了发 `ui.widget.changed` 让界面重新拉一帧。usage-monitor 是第一个用户（`ui.widget.usage.v1`），界面里硬编码的"用量浮窗"（`float.go` + `usage.snapshot` 事件）整个删掉。单测覆盖发现 / 渲染 / 拖拽 / 收起 / 位置夹回屏幕内 / 事件刷新 / 插件没了浮窗跟着没
- [x] **其余插件逐一判断过，各有归属**：`fs-tool`/`svgdraw` 是无头工具；`conversation` 的界面就是聊天页；`builder` 是纯构建服务（无配置项）；`usage-monitor` 的界面是它自带的浮窗（`ui.widget.*`，不是标签页）。都不开标签页
- [x] **清理删除插件残留 + 优化删除**：界面里的 `pluginInfo` 瘦身到只剩认页面用得到的 `id`/`dir`/`provides`/`alive`（`Self`/名字/版本/依赖/权限等 11 个字段随插件页搬走后没人再读，一并删掉，`readPlugin` 也不再白解析那 12 个 manifest 字段）；删除插件时**删干净了就不再写 `pending-restart.json`**（避免重启时白删一遍），`markPending` 对重复记录去重
- [x] **删除插件当场热卸载（`core.unload`）**：核心新增 `core.unload`（停进程 + 摘除 + 注销服务 + 清订阅 + **关日志句柄** + 撤权限 + 广播 `plugin.state`），只认官方插件；删除插件先调它热卸载，成功了目录与日志当场删干净、不用重启（这是规范 2.6"绝不热更新"的唯一例外 —— 卸载比加载安全），卸载失败就退回 `pending-restart.json` 兜底。装新插件仍重启生效。两条自查补上的：**关日志句柄**（`logs.Logger` 惰性打开且默认一直持有，不关的话 Windows 上 `logs/<id>.log` 删不掉，删除还是得重启）；**界面插件不热卸载**（提供 `ui.v1` 的插件跳过卸载 —— 它的进程就是用户正在看的界面，杀掉它用户连删除结果都看不到）。用例覆盖核心 unload（停进程/摘除/注销服务/清订阅）、`ClosePlugin`（关了就能删、不牵连别的插件、nil 安全）、插件管理器"删除运行中插件先卸载、删干净不标 pending"与"界面插件跳过卸载"
- [x] **工具默认写进工作区**：`write_file {"path":"ai-made.txt"}` 落到工作区而非运行时目录；`list_dir` 省略 path 列出的是工作区；绝对路径原样使用
- [x] **预算可自定义 / 可不限制**：`configure {"maxSteps":40}` 生效且不再被硬上限截断；`{"maxSteps":-1}` 后 `info` 明确报「不限制」；重启后仍记得
- [x] **聊天页光标跟随左右键**：移动光标不再被"画面没变就跳过重绘"吞掉，中文按两列宽度定位
- [x] **AI 自建插件（Python）**：`create` 生成目录与 manifest → 重启 → `greet.v1` 注册成功并可调
- [x] **AI 自建插件（Go）**：`create` 写源码 → `builder.v1 build` 编译出 main.exe → 重启 → `stamp.v1` 注册成功
- [x] **create 的格式校验**：非法 id、非法版本、服务名不带版本、重复创建全部 `-32602` 拒绝
- [x] **builder 不接受任意命令**：`../evil.plugin` 被拒；只执行写死的 `go build`，无命令参数入口
- [x] **create 自动注册 AI 工具**：传 `tools` 后新插件立刻出现在 `toolExecutor.v1 list` 里，模型可用
- [x] **用量监控上线**：`com.official.usage` 激活并注册 `usage.v1`，经能力代理读 llm 配置取到密钥，`GET /user/balance -> 200`（真机）
- [x] **计量桥真的接管了**：单测断言 `providers` 里 `openai` 与 `openai-legacy` 指向不同实现，且响应体里的 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` 解析得出来
- [x] **浮层排版不变量**：7 行 × 5 组数据（空 / 有数 / 余额失败 / 超长值 / 未知币种），每行显示宽度精确等于 36 列，且不含转义序列
- [x] **过程框真机抓帧**：自动化驱动核心跑了一次真实提问，抓到的画面里有「第 3 轮 · 完成 · 3 次工具调用 · 用时 6.0s」、三条 `✓ 工具名 · 参数 · 耗时`、以及模型的思考内容
- [x] **过程框排版不变量**：5 组状态 × 3 种宽度（60 / 40 / 24 列），每行宽度精确等于框宽，超长工具名与超长思考都不撑宽行
- [x] **浮窗拖动真机抓帧**：挂到 core 的控制台上注入原生鼠标记录（按下 → 移动 → 松手），浮窗从右上角挪到 (56,7) 并停住，位置落盘 `{"x":56,"y":7,"fold":false}`；另有 SGR 序列一路：喂字节流同样能拖、轻点收起成一条
- [x] **开颜色时浮窗照旧**：真终端必定带颜色，此时覆盖按显示列走、宽度取形态常量 —— 每种尺寸下每行仍是满宽，不因样式码多吃一截
- [x] **按列覆盖不变量**：覆盖混着 ANSI 的行、以及起点正好切在汉字身上的情况 —— 行宽不变、样式接得回、不留半个转义序列

## 尚未实现（对应规范第二、三阶段）

规范 4.3 的「用户最小可用组合」**已经全部就位**（极简 UI / 会话 / Agent / LLM / 工具执行器 / 工具插件）。
下面剩的是生态与生产化部分：

- 整合包 `pack.json` 与 `install-pack`
- 插件市场插件；Node / TypeScript SDK（Go / Python / Java / C 已就绪）
- 规范 2.7 的“语言适配器插件”路线：当前用核心的 `runtime` 字段直接启动各语言运行时，
  没有再套一层适配器插件——核心仍然不认识这些语言，只认识“用什么命令启动”
- 安全沙箱插件（Wasm / Deno）、秘密存储插件
- 绕过能力代理的强制隔离（核心已在能力入口强制校验权限，但无法阻止插件直接调用系统 API）
- 重启式更新与回滚、多租户隔离、遥测

## 已知取舍

- **Windows 重启语义**：没有 `syscall.Exec`，改为派生自身后立即退出（进程数始终为 1）。因此重启后的核心若无可读 stdin，会自动跳过交互式引导菜单继续启动。
- **多了一层启动器**：双击入口和核心成了两个进程、两个 exe。换来的是入口能站在显眼的位置、带图标、能查重；代价是「双击启动器」和「直接跑 `runtime/core.exe`」的行为不完全一样（后者不查重、不改窗口标题）。启动器只做转发，删掉它核心照常能用。
- **格式化控制端口**：重启时旧监听可能仍处于 `TIME_WAIT`，核心会短暂重试最多 2 秒，失败只记录警告，不阻塞启动。
- **插件崩溃不自动重启**：符合 MVP 约定，恢复靠人工重启或后续的守护插件。
- **终端 UI 必须绕开 stdin/stdout**：插件的 stdin/stdout 是 JSON-RPC 硬通道，所以极简 UI 直接打开控制终端（Windows `CONIN$`/`CONOUT$`，Unix `/dev/tty`）渲染，全程不写 stdout。没有可用终端时（例如被当作后台服务启动）自动进入静默模式，核心照常运行。
- **界面相关的环境变量**（只在没有真终端、或自动化驱动时才用得到）：
  - `AIWORK_UI_TTY`：指定终端设备或文件路径以替代默认控制终端
  - `AIWORK_UI_OUT`：把输出写到**另一个**文件。读写同一个文件会让界面读回自己刚画的内容（把边框当成键盘输入），所以自动化驱动必须分开
  - `AIWORK_UI_HOLD_ON_EOF`：输入读到 EOF 后不退出，继续渲染并等待新追加的输入。它让自动化脚本可以分步喂事件——等插件清单加载完再发点击
- **谁接管终端**：两个 UI 都提供 `ui.v1`，而 `ui.v1` 只能有一个所有者。抢不到的那个记一行日志后安静退出，绝不去和另一个抢屏幕；所以默认只把工作台装进 `plugins/`。想换回极简 UI，移走 `runtime/plugins/workbench.plugin` 并装入 `runtime/builtin/minimal-ui.zip` 即可（引导菜单的 `install <路径>` 就支持 zip）。
- **界面上的链路快照**：界面探测到的是**启动那一刻**的快照，而插件是按目录名的字母序逐个激活的。名字排在界面之后的插件（`workspace.plugin` 的 works 在 `workbench` 的 workb 之后）激活时，界面早就扫完了 —— 所以第一帧里的它必然是「未运行」，这不是偶发竞态，是必然。界面收到 `plugin.state` 后会**防抖 300 毫秒再补扫一次**（同一批十几个激活事件合并成一次扫描），自动追平，不需要用户动手：实测**不发任何输入**，`workspace.v1` 会在第 8 帧自己变成 ✓。手动「重新扫描」按钮仍然保留，那一次会出声（状态栏给出反馈）；自动补扫则安静进行，不覆盖用户正在看的提示。聊天链路本身不受影响——`session`/`agent`/`toolExecutor` 在每次调用前都会重新 `core.consume`。
- **识图与生成图是两件事**：`deepseek-flash`（V4.1 Flash）**能识图**（实测可读出截图细节），所以"看图 / 逆向截图"这条链路已经完整可用；但它**只输出文本、不能生成图像**，要"AI 生成图片"必须另接图像生成服务。视频/音频目前一律只传元数据：模型生态还没有通用的视频解析路径，与其对着二进制瞎猜，不如如实说"看不了"。
