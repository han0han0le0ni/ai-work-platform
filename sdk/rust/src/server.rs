//! 主循环：stdin 逐行读 JSON-RPC，stdout 逐行写 JSON-RPC。
//!
//! 硬通道约束：stdin / stdout 属于协议，不是插件的。调试信息只能走
//! [`crate::logf`]（stderr），核心会把它转发到 `logs/<pluginId>.log`。

use std::any::Any;
use std::io::{BufRead, Write};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::{Arc, Mutex};
use std::thread;

use serde_json::{json, Value};

use crate::context::{lock, Context, Inner};
use crate::rpc::{Message, RpcError, RpcResult};

/// 插件作者实现的最小接口。
pub trait Plugin: Send + 'static {
    /// 收到 `core.init` 后调用：在这里注册服务、订阅事件。
    fn activate(&mut self, ctx: &Context) -> RpcResult<()>;

    /// 处理一次 `core.invoke`。`args` 可能是 `{}` 或 `null`。
    fn handle(
        &mut self,
        ctx: &Context,
        service: &str,
        method: &str,
        args: Value,
    ) -> RpcResult<Value>;

    /// 收到 `core.shutdown` 时调用，可做清理。
    ///
    /// 此时读线程已经停了，**不要**在这里再向核心发起调用（会等不到响应）。
    fn deactivate(&mut self, _ctx: &Context) {}
}

/// 用进程的 stdin / stdout 运行插件，直到 stdin 关闭或核心要求退出。
pub fn serve<P: Plugin>(plugin: P) -> RpcResult<()> {
    serve_io(plugin, std::io::stdin().lock(), std::io::stdout())
}

/// 与 [`serve`] 相同，但通道由调用方提供 —— 单测里换成内存管道即可。
pub fn serve_io<P, R, W>(plugin: P, reader: R, writer: W) -> RpcResult<()>
where
    P: Plugin,
    R: BufRead,
    W: Write + Send + 'static,
{
    let inner = Arc::new(Inner::new(Box::new(writer)));
    let ctx = Context::new(inner);
    let plugin = Arc::new(Mutex::new(plugin));
    run(plugin, ctx, reader)
}

fn run<P, R>(plugin: Arc<Mutex<P>>, ctx: Context, mut reader: R) -> RpcResult<()>
where
    P: Plugin,
    R: BufRead,
{
    let mut buf = String::new();
    loop {
        buf.clear();
        match reader.read_line(&mut buf) {
            Ok(0) => return Ok(()), // stdin 关了：核心不要我们了
            Ok(_) => {}
            Err(e) => {
                crate::logf("error", format!("读取 stdin 失败: {e}"));
                return Ok(());
            }
        }

        let line = buf.trim();
        if line.is_empty() {
            continue;
        }
        let msg: Message = match serde_json::from_str(line) {
            Ok(m) => m,
            Err(e) => {
                crate::logf("error", format!("无法解析来自核心的消息: {e}"));
                continue;
            }
        };

        let Some(method) = msg.method.clone() else {
            // method 为空 = 核心对我们某次调用的响应
            ctx.deliver(&msg);
            continue;
        };

        if method == "core.shutdown" {
            let mut guard = lock(&plugin);
            guard.deactivate(&ctx);
            return Ok(());
        }

        if msg.id.is_some() {
            // 每个请求一个线程：handler 里调用 core 时，读线程仍能收回响应
            let plugin = Arc::clone(&plugin);
            let ctx = ctx.clone();
            thread::spawn(move || handle_request(plugin, ctx, msg));
        } else {
            let ctx = ctx.clone();
            thread::spawn(move || handle_notification(ctx, msg));
        }
    }
}

fn handle_request<P: Plugin>(plugin: Arc<Mutex<P>>, ctx: Context, msg: Message) {
    let id = msg.id.clone().unwrap_or(Value::Null);
    let method = msg.method.clone().unwrap_or_default();
    let params = msg.params.clone().unwrap_or(Value::Null);

    match method.as_str() {
        "core.init" => {
            ctx.apply_init(&params);
            let outcome = catch_unwind(AssertUnwindSafe(|| {
                let mut guard = lock(&plugin);
                guard.activate(&ctx)
            }));
            match outcome {
                Ok(Ok(())) => respond(&ctx, id, json!({ "ready": true })),
                Ok(Err(e)) => respond_error(&ctx, id, e),
                Err(payload) => {
                    let detail = panic_text(payload.as_ref());
                    crate::logf("error", format!("处理 core.init 时 panic: {detail}"));
                    respond_error(&ctx, id, RpcError::internal(format!("插件 panic: {detail}")));
                }
            }
        }

        "core.invoke" => {
            let service = str_at(&params, "service");
            let call = str_at(&params, "method");
            let args = match params.get("args") {
                None | Some(Value::Null) => json!({}),
                Some(v) => v.clone(),
            };
            let outcome = catch_unwind(AssertUnwindSafe(|| {
                let mut guard = lock(&plugin);
                guard.handle(&ctx, &service, &call, args)
            }));
            match outcome {
                Ok(Ok(v)) => respond(&ctx, id, v),
                Ok(Err(e)) => respond_error(&ctx, id, e),
                Err(payload) => {
                    let detail = panic_text(payload.as_ref());
                    crate::logf(
                        "error",
                        format!("处理 {service}.{call} 时 panic: {detail}"),
                    );
                    respond_error(&ctx, id, RpcError::internal(format!("插件 panic: {detail}")));
                }
            }
        }

        other => respond_error(
            &ctx,
            id,
            RpcError::method_not_found(format!("插件不支持方法 {other}")),
        ),
    }
}

fn handle_notification(ctx: Context, msg: Message) {
    let method = msg.method.clone().unwrap_or_default();
    match method.as_str() {
        "core.event" => {
            let params = msg.params.clone().unwrap_or(Value::Null);
            let event = str_at(&params, "event");
            let payload = params.get("payload").cloned().unwrap_or(Value::Null);
            for handler in ctx.handlers_for(&event) {
                let ctx = ctx.clone();
                let payload = payload.clone();
                let event_name = event.clone();
                thread::spawn(move || {
                    if let Err(p) = catch_unwind(AssertUnwindSafe(|| handler(&ctx, payload))) {
                        crate::logf(
                            "error",
                            format!(
                                "处理事件 {event_name} 时 panic: {}",
                                panic_text(p.as_ref())
                            ),
                        );
                    }
                });
            }
        }
        other => crate::logf("warn", format!("收到未知通知 {other}")),
    }
}

fn respond(ctx: &Context, id: Value, result: Value) {
    if let Err(e) = ctx.send_message(&Message::response(id, result)) {
        crate::logf("error", format!("回写响应失败: {e}"));
    }
}

fn respond_error(ctx: &Context, id: Value, err: RpcError) {
    if let Err(e) = ctx.send_message(&Message::error_response(id, err)) {
        crate::logf("error", format!("回写错误响应失败: {e}"));
    }
}

fn str_at(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn panic_text(payload: &(dyn Any + Send)) -> String {
    if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_string()
    } else if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else {
        "未知 panic".to_string()
    }
}
