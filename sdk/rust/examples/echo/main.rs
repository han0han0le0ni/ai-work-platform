//! Rust SDK 示例插件：提供一个 `echoRust.v1` 服务，原样回显收到的 `text`。
//!
//! 构建与安装：
//!
//! ```sh
//! cargo build --release --example echo
//! ```
//!
//! 把 `target/release/examples/echo.exe`（非 Windows 为 `echo`）改名成 `main`，
//! 与同目录的 `manifest.json` 一起放进 `runtime/plugins/echo-rust.plugin/` 即可加载。

use std::process::ExitCode;

use aiwork_plugin::{json, serve, Context, Plugin, RpcError, RpcResult, Value};

struct Echo;

impl Plugin for Echo {
    fn activate(&mut self, ctx: &Context) -> RpcResult<()> {
        ctx.provide(
            "echoRust.v1",
            json!({
                "description": "Rust SDK 示例：原样回显输入",
                "methods": ["run"],
            }),
        )?;
        ctx.log("info", "Rust echo 插件已就绪", None);
        Ok(())
    }

    fn handle(
        &mut self,
        _ctx: &Context,
        service: &str,
        method: &str,
        args: Value,
    ) -> RpcResult<Value> {
        if service != "echoRust.v1" {
            return Err(RpcError::service_not_found(format!("没有服务 {service}")));
        }
        match method {
            "run" => Ok(json!({
                "echo": args.get("text").cloned().unwrap_or(Value::Null),
                "lang": "rust",
            })),
            other => Err(RpcError::method_not_found(format!("不支持的方法 {other}"))),
        }
    }

    fn deactivate(&mut self, ctx: &Context) {
        ctx.log("info", "Rust echo 插件已退出", None);
    }
}

fn main() -> ExitCode {
    match serve(Echo) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            aiwork_plugin::logf("error", format!("Rust echo 插件退出: {e}"));
            ExitCode::FAILURE
        }
    }
}
