//! AI Work Platform 的 Rust 插件 SDK。
//!
//! 核心完全不感知插件用什么语言写：它只看到 **JSON-RPC over stdio** 和一个
//! `manifest.json`。这个 crate 把那套协议翻译成普通的 Rust 方法调用。
//!
//! # 最小示例
//!
//! ```no_run
//! use aiwork_plugin::{json, serve, Context, Plugin, RpcError, RpcResult, Value};
//!
//! struct Echo;
//!
//! impl Plugin for Echo {
//!     fn activate(&mut self, ctx: &Context) -> RpcResult<()> {
//!         ctx.provide("echoRust.v1", json!({ "description": "回显" }))?;
//!         Ok(())
//!     }
//!
//!     fn handle(
//!         &mut self,
//!         _ctx: &Context,
//!         service: &str,
//!         method: &str,
//!         args: Value,
//!     ) -> RpcResult<Value> {
//!         if service != "echoRust.v1" {
//!             return Err(RpcError::service_not_found("没有这个服务"));
//!         }
//!         if method != "run" {
//!             return Err(RpcError::method_not_found("没有这个方法"));
//!         }
//!         Ok(json!({ "echo": args.get("text").cloned().unwrap_or(Value::Null) }))
//!     }
//! }
//!
//! fn main() {
//!     serve(Echo).expect("插件异常退出");
//! }
//! ```
//!
//! # 硬通道约束（最容易踩的坑）
//!
//! **stdin / stdout 属于协议，不是你的。**
//!
//! 想打印调试信息，只能用 [`logf`]（走 stderr），核心会把 stderr 转发到
//! `logs/<pluginId>.log`。任何 `println!` 都会污染协议，轻则那条消息被丢弃，
//! 重则插件被判定为死进程。
//!
//! # 线程与重入模型
//!
//! 读线程只负责读消息和分发，**每个请求 / 事件各起一个线程**执行。
//! `Plugin` 本体放在 `Mutex` 里，所以同一个插件的 handler 是串行执行的 ——
//! 换来的是 `&mut self` 的普通写法，不需要写 `unsafe`。
//! handler 里可以放心 [`Context::invoke`]：响应由读线程收，不会死锁。
//!
//! 代价：不要在 handler 里做长时间阻塞（会挡住同一插件的其他请求）。

mod base64;
mod context;
mod rpc;
mod server;

pub use context::{ConsumeResult, Context, EventHandler};
pub use rpc::{
    RpcError, RpcResult, CODE_API_VERSION_INCOMPATIBLE, CODE_DEPENDENCY_MISSING,
    CODE_INTERNAL_ERROR, CODE_INVALID_PARAMS, CODE_INVALID_REQUEST, CODE_METHOD_NOT_FOUND,
    CODE_PARSE_ERROR, CODE_PERMISSION_DENIED, CODE_PROVIDER_DEAD, CODE_SANDBOX_UNAVAILABLE,
    CODE_SERVICE_NOT_FOUND,
};
pub use serde_json::{json, Map, Value};
pub use server::{serve, serve_io, Plugin};

/// 写插件自身的 stderr 日志，核心会把它转发到 `logs/<pluginId>.log`。
///
/// 这是唯一允许的调试输出方式：stdout 属于 JSON-RPC 通道。
pub fn logf(level: &str, msg: impl AsRef<str>) {
    eprintln!("[{}] {}", level.to_uppercase(), msg.as_ref());
}
