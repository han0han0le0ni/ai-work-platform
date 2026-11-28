//! 协议级集成测试：不启动核心，用内存管道模拟一次 core.init + core.invoke。

use std::io::{self, Cursor, Write};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use aiwork_plugin::{json, serve_io, Context, Plugin, RpcError, RpcResult, Value};

struct TestPlugin;

impl Plugin for TestPlugin {
    fn activate(&mut self, ctx: &Context) -> RpcResult<()> {
        // core.init 下发的内容必须在 Activate 之前就位
        assert_eq!(ctx.plugin_id(), "com.test.sdk");
        assert_eq!(ctx.namespace(), "com.test.sdk");
        assert_eq!(ctx.api_version(), "1");
        assert!(ctx.can("filesystem:read"));
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
            return Err(RpcError::service_not_found("没有这个服务"));
        }
        match method {
            "run" => Ok(json!({ "echo": args.get("text").cloned().unwrap_or(Value::Null) })),
            other => Err(RpcError::method_not_found(format!("没有方法 {other}"))),
        }
    }
}

/// 可跨线程查看的输出缓冲。
#[derive(Clone, Default)]
struct SharedBuf(Arc<Mutex<Vec<u8>>>);

impl Write for SharedBuf {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(buf);
        Ok(buf.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

fn wait_for(shared: &SharedBuf, needle: &str) -> String {
    let deadline = Instant::now() + Duration::from_secs(3);
    loop {
        let text = String::from_utf8_lossy(&shared.0.lock().unwrap()).to_string();
        if text.contains(needle) {
            return text;
        }
        if Instant::now() > deadline {
            panic!("等待 {needle} 超时，当前输出：{text}");
        }
        thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn handshake_and_invoke() {
    let input = concat!(
        r#"{"jsonrpc":"2.0","id":0,"method":"core.init","params":{"pluginId":"com.test.sdk","apiVersion":"1","coreVersion":"1.0.0","configNamespace":"com.test.sdk","permissions":["filesystem:read"]}}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":1,"method":"core.invoke","params":{"service":"echoRust.v1","method":"run","args":{"text":"你好"}}}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":2,"method":"core.invoke","params":{"service":"echoRust.v1","method":"nope","args":null}}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":3,"method":"core.whatever"}"#,
        "\n"
    );

    let out = SharedBuf::default();
    serve_io(TestPlugin, Cursor::new(input), out.clone()).expect("serve 应正常退出");

    let text = wait_for(&out, "\"id\":3");
    assert!(text.contains(r#""id":0,"result":{"ready":true}"#), "{text}");
    assert!(text.contains(r#""echo":"你好""#), "{text}");
    assert!(text.contains(r#""id":2,"error":{"code":-32601"#), "{text}");
    assert!(text.contains(r#""id":3,"error":{"code":-32601"#), "{text}");
}

#[test]
fn missing_args_defaults_to_empty_object() {
    let input = concat!(
        r#"{"jsonrpc":"2.0","id":0,"method":"core.init","params":{"pluginId":"com.test.sdk"}}"#,
        "\n",
        r#"{"jsonrpc":"2.0","id":1,"method":"core.invoke","params":{"service":"echoRust.v1","method":"run"}}"#,
        "\n"
    );

    let out = SharedBuf::default();
    serve_io(TestPlugin, Cursor::new(input), out.clone()).expect("serve 应正常退出");

    let text = wait_for(&out, "\"id\":1");
    assert!(text.contains(r#""result":{"echo":null}"#), "{text}");
}
