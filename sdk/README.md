# 插件 SDK

五套 SDK，同一份协议。**核心完全不感知插件是用什么语言写的**——它只看到 JSON-RPC over stdio 和一个 `manifest.json`。

```
sdk/
├── go/plugin/      Go      —— 编译成原生二进制，runtime: binary
├── rust/           Rust    —— 编译成原生二进制，runtime: binary
├── python/         Python  —— 单文件，runtime: python
├── java/           Java    —— 打成 jar，runtime: java
└── c/              C99     —— 编译成原生二进制，runtime: binary
```

## 共同契约

每个 SDK 都做同样四件事：

1. 从 **stdin** 逐行读 JSON-RPC，往 **stdout** 逐行写 JSON-RPC
2. 处理四个核心消息：`core.init` / `core.invoke` / `core.event` / `core.shutdown`
3. 通过上下文 `ctx` 暴露核心的 6 个基础接口 + 能力代理
4. 出站调用要有自己的 id，并在响应回来之前等住

### 硬通道约束（最容易踩的坑）

> **stdin / stdout 属于协议，不是你的。**
>
> 想打印调试信息，只能用 `ctx.Log()` / `Logf()` / `aw_log()`——它们走 **stderr**，核心会把 stderr 转发到 `logs/<pluginId>.log`。任何 `print` / `System.out.println` / `printf` 都会污染协议，轻则该条消息被丢弃，重则插件被判定为死进程。

### 线程与重入模型

这是四套 SDK 里唯一有实质差异的地方，也是必须做对的地方：

| SDK | 模型 | 说明 |
|---|---|---|
| Go | 读协程 + 每请求一协程 | handler 里可以直接 `Invoke`，响应由读协程收 |
| Rust | 读线程 + 每请求一线程 | `Plugin` 放在 `Mutex` 里，同一插件的 handler 串行；handler 里可直接 `invoke` |
| Python | 读线程 + 派发线程 + 每请求一线程 | 读线程只投递，绝不做业务 |
| Java | 读线程 + 派发线程 + 线程池 | 同上 |
| C | 单线程 + 可重入泵 | `aw_invoke` 会就地“泵”消息，期间到达的请求被递归处理 |

为什么不能图省事：如果读和派发在同一个线程上，handler 里的 `ctx.invoke` 会让当前线程阻塞等待响应——而那个响应正是这个线程该去读的，于是**死锁**。C 版因为单线程，用可重入泵绕开；代价是不要在 handler 里做长阻塞。

### 错误传递

handler 里报错要让调用方拿到**原始错误码**，不要包装成内部错误：

```go
return nil, plugin.Errorf(plugin.CodeServiceNotFound, "...")   // Go
raise RpcError(CODE_SERVICE_NOT_FOUND, "...")                  // Python
throw new RpcError(RpcError.SERVICE_NOT_FOUND, "...");         // Java
aw_fail(ctx, AW_SERVICE_NOT_FOUND, "..."); return NULL;        // C
```

## API 对照

| 能力 | Go | Python | Java | C | Rust |
|---|---|---|---|---|---|
| 入口 | `plugin.Serve(&X{})` | `serve(X())` | `Serve.run(new X())` | `aw_serve(&plugin)` | `serve(Echo)` |
| 激活 | `Activate(ctx)` | `activate(ctx)` | `activate(ctx)` | `plugin.activate` | `activate(&mut self, ctx)` |
| 处理调用 | `Handle(s,m,args)` | `handle(s,m,args)` | `handle(s,m,args)` | `plugin.handle` | `handle(ctx,s,m,args)` |
| 退出清理 | `Deactivate()` | `deactivate()` | `deactivate()` | `plugin.deactivate` | `deactivate(&mut self, ctx)` |
| 注册服务 | `ctx.Provide(n,meta)` | `ctx.provide(n,meta)` | `ctx.provide(n,meta)` | `aw_provide(ctx,n,meta)` | `ctx.provide(n,meta)` |
| 发现服务 | `ctx.Consume(n)` | `ctx.consume(n)` | `ctx.consume(n)` | `aw_consume(ctx,n)` | `ctx.consume(n)` |
| 调用服务 | `ctx.Invoke(s,m,args)` | `ctx.invoke(s,m,args)` | `ctx.invoke(s,m,args)` | `aw_invoke(ctx,s,m,args)` | `ctx.invoke(s,m,args)` |
| 发布事件 | `ctx.Emit(e,p)` | `ctx.emit(e,p)` | `ctx.emit(e,p)` | `aw_emit(ctx,e,p)` | `ctx.emit(e,p)` |
| 订阅事件 | `ctx.On(e,h)` | `ctx.on(e,h)` | `ctx.on(e,h)` | `aw_on(ctx,e,fn)` | `ctx.on(e, 闭包)` |
| 读配置 | `ctx.GetConfig(k)` | `ctx.get_config(k)` | `ctx.getConfig(k,def)` | `aw_get_config(ctx,k)` | `ctx.get_config(k)` |
| 写配置 | `ctx.SetConfig(k,v)` | `ctx.set_config(k,v)` | `ctx.setConfig(k,v)` | `aw_set_config(ctx,k,v)` | `ctx.set_config(k,v)` |
| 日志 | `ctx.Log(l,m,meta)` | `ctx.log(l,m,meta)` | `ctx.log(l,m,meta)` | `aw_log(ctx,l,m)` | `ctx.log(l,m,None)` |
| 能力代理 | `ctx.Capability(m,p)` | `ctx.capability(m,p)` | `ctx.capability(m,p)` | `aw_capability(ctx,m,p)` | `ctx.capability(m,p)` |
| 沙箱 | `ctx.SandboxRun(...)` | `ctx.sandbox_run(...)` | `ctx.sandboxRun(...)` | `aw_sandbox_run(...)` | `ctx.sandbox_run(...)` |

能力代理还额外提供 `fsRead` / `fsWrite` / `fsList` / `netRequest` / `spawn` 的便捷封装。

> 能力代理是**权限闸门**：必须在 `manifest.json` 的 `permissions` 里声明（`filesystem:read`、`filesystem:write`、`network`、`process:spawn`），否则核心一律返回 `-32002 权限不足`。

## 各语言快速开始

### Go

```go
type Echo struct{ ctx *plugin.Context }

func (e *Echo) Activate(ctx *plugin.Context) error {
	e.ctx = ctx
	return ctx.Provide("echo.v1", map[string]any{"description": "回显"})
}
func (e *Echo) Handle(service, method string, args json.RawMessage) (any, error) {
	return map[string]any{"echo": "..."}, nil
}
func (e *Echo) Deactivate() error { return nil }

func main() { plugin.Serve(&Echo{}) }
```

```json
{ "entry": "main", "runtime": "binary", "provides": ["echo.v1"] }
```

`go build -o <插件目录>/main ./你的插件` 即可。

### Python

```python
from aiwork_plugin import Plugin, RpcError, serve, CODE_METHOD_NOT_FOUND

class Echo(Plugin):
    def activate(self, ctx):
        self.ctx = ctx
        ctx.provide("echo.v1", {"description": "回显"})
    def handle(self, service, method, args):
        if method != "run":
            raise RpcError(CODE_METHOD_NOT_FOUND, "不支持的方法")
        return {"echo": (args or {}).get("text", "")}
    def deactivate(self):
        self.ctx.log("info", "再见")

if __name__ == "__main__":
    serve(Echo())
```

```json
{ "entry": "main.py", "runtime": "python", "provides": ["echo.v1"] }
```

把 `sdk/python/aiwork_plugin.py` 和 `main.py` 放在同一目录（插件包自包含），核心会用 `python -u main.py` 启动它。

### Java

```java
public final class Echo implements Plugin {
    private Context ctx;
    public void activate(Context ctx) {
        this.ctx = ctx;
        ctx.provide("echo.v1", Json.map("description", "回显"));
    }
    public Object handle(String service, String method, Map<String, Object> args) {
        if (!"run".equals(method)) throw new RpcError(RpcError.METHOD_NOT_FOUND, "不支持的方法");
        return Json.map("echo", Json.asString(args.get("text"), ""));
    }
    public static void main(String[] args) { Serve.run(new Echo()); }
}
```

```json
{ "entry": "plugin.jar", "runtime": "java", "provides": ["echo.v1"] }
```

打包（SDK class 一起打进去，插件包才是自包含的）：

```sh
javac -encoding UTF-8 -d build/sdk sdk/java/src/aiwork/plugin/*.java
javac -encoding UTF-8 -cp build/sdk -d build/plugin 你的源码...
cp -r build/sdk/* build/plugin/
jar --create --file plugin.jar --main-class com.example.Echo -C build/plugin .
```

`entry` 也可以写**类名**（如 `com.example.Echo`），核心会用 `java -cp <插件目录> <类名>` 启动——只要 `.class` 树就在插件目录下。

### C

```c
#include "aiwork_plugin.h"

static void on_activate(aw_ctx *ctx) { aw_provide(ctx, "echo.v1", NULL); }

static aj_value *on_handle(aw_ctx *ctx, const char *service,
                           const char *method, const aj_value *args) {
    aj_value *out;
    if (strcmp(method, "run") != 0) {
        aw_fail(ctx, AW_METHOD_NOT_FOUND, "不支持的方法");
        return NULL;
    }
    out = aj_obj();
    aj_obj_set(out, "echo", aj_str(aj_get_str(args, "text", "")));
    return out;   /* SDK 负责释放 */
}

int main(void) {
    aw_plugin plugin = { on_activate, on_handle, NULL };
    return aw_serve(&plugin);
}
```

```json
{ "entry": "main", "runtime": "binary", "provides": ["echo.v1"] }
```

编译（把 SDK 的两个文件一起编进去）：

```sh
gcc -std=c99 -O2 -I sdk/c -o main main.c sdk/c/aiwork_plugin.c
tcc -I sdk/c -o main.exe main.c sdk/c/aiwork_plugin.c
```

内存规则：**构造函数（`aj_obj` / `aj_str` / ...）产出的值所有权转移给容器；`aj_invoke` / `aw_get_config` 返回的值由调用方 `aj_free`；handler 的返回值由 SDK 释放。**

### Rust

```rust
use aiwork_plugin::{json, serve, Context, Plugin, RpcError, RpcResult, Value};

struct Echo;

impl Plugin for Echo {
    fn activate(&mut self, ctx: &Context) -> RpcResult<()> {
        ctx.provide("echoRust.v1", json!({ "description": "回显" }))
    }
    fn handle(&mut self, _ctx: &Context, service: &str, method: &str, args: Value) -> RpcResult<Value> {
        if method != "run" {
            return Err(RpcError::method_not_found("不支持的方法"));
        }
        Ok(json!({ "echo": args.get("text").cloned().unwrap_or(Value::Null) }))
    }
}

fn main() { serve(Echo).expect("插件异常退出"); }
```

```json
{ "entry": "main", "runtime": "binary", "provides": ["echoRust.v1"] }
```

引用 SDK（`sdk/rust` 是一个普通 crate，按路径依赖即可）：

```toml
[dependencies]
aiwork-plugin = { path = "../../sdk/rust" }
serde_json = "1"
```

```sh
cargo build --release
# Windows 拷 main.exe，其他平台拷 main
cp target/release/你的插件 runtime/plugins/echo-rust.plugin/main
```

仓库里有一份可直接跑的示例：`sdk/rust/examples/echo/`。
`cargo build --release --example echo` 会在 `target/release/examples/` 下产出二进制；
`build.ps1` 检测到 `cargo` 时会顺手把它装成 `runtime/plugins/echo-rust.plugin/`。

## runtime 字段

| runtime | 核心如何启动插件 |
|---|---|
| `binary` | 直接执行 `entry`（Windows 自动补 `.exe` / `.cmd` / `.bat`） |
| `python` | `python -u <entry>.py` |
| `node` | `node <entry>.js` |
| `java` | `entry` 是 jar → `java -jar`；是类名 → `java -cp <插件目录> <类名>` |
| `wasm` | 需要适配器插件，核心不直接加载 |

解释器路径可用环境变量覆盖：`AIWORK_PYTHON`、`AIWORK_JAVA`、`AIWORK_NODE`。

## 示例插件

仓库里的官方插件目前都是 Go 写的（`fs-tool` / `llm` / `agent` / `session` / `workbench` / `plugin-manager` / `workspace`）。
上面各语言那一小段 `Echo` 代码，就是对应 SDK 的**最小示例** —— 照抄即可得到一个能加载的插件，
核心不关心它用什么语言写：把任一语言的 `Echo` 编出来放进 `plugins/<名字>.plugin/` 就能加载，
五种语言走的是同一份协议。
