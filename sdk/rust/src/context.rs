//! Context：插件与核心交互的唯一入口。
//!
//! 它只暴露核心的 6 个基础接口 + 能力代理，不暴露任何核心内部实现。

use std::collections::HashMap;
use std::io::Write;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Mutex, MutexGuard, RwLock};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::base64;
use crate::rpc::{Message, RpcError, RpcResult};

/// 事件处理函数：拿到当前 Context 与事件 payload。
pub type EventHandler = Arc<dyn Fn(&Context, Value) + Send + Sync>;

/// 核心在 `core.init` 里下发的插件身份与权限。
#[derive(Debug, Clone, Default)]
pub(crate) struct Identity {
    pub plugin_id: String,
    pub api_version: String,
    pub core_version: String,
    pub namespace: String,
    pub permissions: Vec<String>,
}

/// 进程内共享的通道状态：输出、待回响应表、请求自增 id、身份、事件订阅。
pub(crate) struct Inner {
    out: Mutex<Box<dyn Write + Send>>,
    pending: Mutex<HashMap<String, Sender<Message>>>,
    next_id: AtomicI64,
    identity: RwLock<Identity>,
    handlers: Mutex<HashMap<String, Vec<EventHandler>>>,
}

impl Inner {
    pub(crate) fn new(out: Box<dyn Write + Send>) -> Self {
        Self {
            out: Mutex::new(out),
            pending: Mutex::new(HashMap::new()),
            next_id: AtomicI64::new(0),
            identity: RwLock::new(Identity::default()),
            handlers: Mutex::new(HashMap::new()),
        }
    }
}

/// 取锁时忽略「锁被 panic 污染」：插件自己 panic 不该让整条通道死掉。
pub(crate) fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// `core.consume` 的返回。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConsumeResult {
    #[serde(rename = "pluginId")]
    pub plugin_id: String,
    #[serde(default)]
    pub alive: bool,
}

/// 插件与核心交互的句柄，可克隆，可在多个线程里同时使用。
#[derive(Clone)]
pub struct Context {
    inner: Arc<Inner>,
}

impl Context {
    pub(crate) fn new(inner: Arc<Inner>) -> Self {
        Self { inner }
    }

    // -----------------------------------------------------------------------
    // 身份与权限
    // -----------------------------------------------------------------------

    /// 插件 ID（例如 `com.example.echo-rust`）。
    pub fn plugin_id(&self) -> String {
        self.identity().plugin_id
    }

    /// 核心实现的插件协议版本。
    pub fn api_version(&self) -> String {
        self.identity().api_version
    }

    /// 核心版本。
    pub fn core_version(&self) -> String {
        self.identity().core_version
    }

    /// 配置命名空间：`GetConfig` / `SetConfig` 的键都落在它下面。
    pub fn namespace(&self) -> String {
        self.identity().namespace
    }

    /// manifest 声明的权限清单。
    pub fn permissions(&self) -> Vec<String> {
        self.identity().permissions
    }

    /// 自检某个权限是否被授予：先问一句，比撞上 -32002 再解释好。
    pub fn can(&self, perm: &str) -> bool {
        self.permissions().iter().any(|p| p == perm)
    }

    fn identity(&self) -> Identity {
        self.inner
            .identity
            .read()
            .unwrap_or_else(|e| e.into_inner())
            .clone()
    }

    pub(crate) fn apply_init(&self, params: &Value) {
        let mut id = self
            .inner
            .identity
            .write()
            .unwrap_or_else(|e| e.into_inner());
        id.plugin_id = str_field(params, "pluginId");
        id.api_version = str_field(params, "apiVersion");
        id.core_version = str_field(params, "coreVersion");
        id.namespace = str_field(params, "configNamespace");
        if id.namespace.is_empty() {
            id.namespace = id.plugin_id.clone();
        }
        id.permissions = params
            .get("permissions")
            .and_then(Value::as_array)
            .map(|arr| {
                arr.iter()
                    .filter_map(|v| v.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default();
    }

    // -----------------------------------------------------------------------
    // ① 服务注册与发现
    // -----------------------------------------------------------------------

    /// 注册服务。服务名必须带版本，例如 `echoRust.v1`。
    pub fn provide(&self, name: &str, meta: Value) -> RpcResult<Value> {
        self.call(
            "core.provide",
            json!({ "name": name, "meta": meta }),
            Some(Duration::from_secs(10)),
        )
    }

    /// 发现服务，返回提供者插件 ID 与存活状态。
    pub fn consume(&self, name: &str) -> RpcResult<ConsumeResult> {
        let v = self.call(
            "core.consume",
            json!({ "name": name }),
            Some(Duration::from_secs(10)),
        )?;
        serde_json::from_value(v)
            .map_err(|e| RpcError::internal(format!("解析 core.consume 响应失败: {e}")))
    }

    // -----------------------------------------------------------------------
    // ② 服务调用
    // -----------------------------------------------------------------------

    /// 调用另一个插件提供的服务。
    pub fn invoke(&self, service: &str, method: &str, args: Value) -> RpcResult<Value> {
        self.call(
            "core.invoke",
            json!({ "service": service, "method": method, "args": args }),
            None,
        )
    }

    // -----------------------------------------------------------------------
    // ③ 事件总线
    // -----------------------------------------------------------------------

    /// 发布事件。事件名必须带命名空间，例如 `chat.message`。
    pub fn emit(&self, event: &str, payload: Value) -> RpcResult<()> {
        self.notify("core.emit", json!({ "event": event, "payload": payload }))
    }

    /// 订阅事件。`"*"` 表示订阅全部。
    ///
    /// 这里必须**同时**做两件事：把处理函数记在本地，并告诉核心「我要这个事件」。
    /// 只记本地是不行的 —— 核心只投给登记过的订阅者。
    pub fn on<F>(&self, event: &str, handler: F)
    where
        F: Fn(&Context, Value) + Send + Sync + 'static,
    {
        lock(&self.inner.handlers)
            .entry(event.to_string())
            .or_default()
            .push(Arc::new(handler));

        if let Err(e) = self.call(
            "core.on",
            json!({ "event": event }),
            Some(Duration::from_secs(10)),
        ) {
            crate::logf("warn", format!("订阅事件 {event} 失败: {e}"));
        }
    }

    // -----------------------------------------------------------------------
    // ④ 配置管理
    // -----------------------------------------------------------------------

    /// 读取配置。键不存在时返回 `None`。
    pub fn get_config(&self, key: &str) -> RpcResult<Option<Value>> {
        let v = self.call(
            "core.getConfig",
            json!({ "key": key }),
            Some(Duration::from_secs(10)),
        )?;
        let found = v.get("found").and_then(Value::as_bool).unwrap_or(false);
        if !found {
            return Ok(None);
        }
        match v.get("value") {
            None | Some(Value::Null) => Ok(None),
            Some(val) => Ok(Some(val.clone())),
        }
    }

    /// 读取字符串配置，缺失时返回 `default`。
    pub fn get_config_string(&self, key: &str, default: &str) -> String {
        match self.get_config(key) {
            Ok(Some(Value::String(s))) => s,
            Ok(Some(Value::Number(n))) => n.to_string(),
            _ => default.to_string(),
        }
    }

    /// 读取整数配置，缺失时返回 `default`。
    pub fn get_config_int(&self, key: &str, default: i64) -> i64 {
        match self.get_config(key) {
            Ok(Some(Value::Number(n))) => n.as_i64().unwrap_or(default),
            Ok(Some(Value::String(s))) => s.trim().parse().unwrap_or(default),
            _ => default,
        }
    }

    /// 写入配置。配置键只增不减。
    pub fn set_config(&self, key: &str, value: Value) -> RpcResult<Value> {
        self.call(
            "core.setConfig",
            json!({ "key": key, "value": value }),
            Some(Duration::from_secs(10)),
        )
    }

    // -----------------------------------------------------------------------
    // ⑤ 日志
    // -----------------------------------------------------------------------

    /// 上报日志。核心写入 `logs/<pluginId>.log` 并广播 `core.log` 事件。
    pub fn log(&self, level: &str, msg: &str, meta: Option<Value>) {
        let payload = json!({
            "level": level,
            "msg": msg,
            "meta": meta.unwrap_or(Value::Null),
        });
        if let Err(e) = self.notify("core.log", payload) {
            crate::logf(level, format!("{msg} (上报失败: {e})"));
        }
    }

    // -----------------------------------------------------------------------
    // ⑥ 沙箱（接口预留）
    // -----------------------------------------------------------------------

    /// 请求沙箱执行代码。未安装沙箱插件时核心返回 -32003。
    pub fn sandbox_run(&self, code: &str, options: Value) -> RpcResult<Value> {
        self.call(
            "core.sandbox.run",
            json!({ "code": code, "options": options }),
            None,
        )
    }

    // -----------------------------------------------------------------------
    // 秘密存储（接口预留）
    // -----------------------------------------------------------------------

    /// 读取密钥。未安装 `secrets.v1` 插件时核心返回 -32000。
    pub fn get_secret(&self, key: &str) -> RpcResult<Value> {
        self.call(
            "core.getSecret",
            json!({ "key": key }),
            Some(Duration::from_secs(10)),
        )
    }

    /// 写入密钥。
    pub fn set_secret(&self, key: &str, value: &str) -> RpcResult<Value> {
        self.call(
            "core.setSecret",
            json!({ "key": key, "value": value }),
            Some(Duration::from_secs(10)),
        )
    }

    // -----------------------------------------------------------------------
    // 能力代理（规范 3.5）
    //
    // 权限必须在 manifest 的 permissions 里声明，否则核心一律返回 -32002。
    // -----------------------------------------------------------------------

    /// 直接调用能力代理的任意方法。
    pub fn capability(&self, method: &str, params: Value) -> RpcResult<Value> {
        self.call(method, params, None)
    }

    /// 经核心读文件（需 `filesystem:read`），返回 `{path,size,content,truncated}`。
    pub fn fs_read(&self, path: &str) -> RpcResult<Value> {
        self.capability("core.fs.read", json!({ "path": path }))
    }

    /// 按 base64 读文件（需 `filesystem:read`）。
    ///
    /// 读图片、音视频等二进制必须用它：默认的 `fs_read` 会把无效 UTF-8 字节换成 U+FFFD。
    pub fn fs_read_base64(&self, path: &str) -> RpcResult<Value> {
        self.capability(
            "core.fs.read",
            json!({ "path": path, "encoding": "base64" }),
        )
    }

    /// 经核心写文件（需 `filesystem:write`），返回 `{path,size,append}`。
    pub fn fs_write(
        &self,
        path: &str,
        content: &str,
        append: bool,
        mkdir: bool,
    ) -> RpcResult<Value> {
        self.capability(
            "core.fs.write",
            json!({ "path": path, "content": content, "append": append, "mkdir": mkdir }),
        )
    }

    /// 按 base64 写二进制文件（需 `filesystem:write`），返回 `{path,size}`。
    pub fn fs_write_bytes(&self, path: &str, data: &[u8], mkdir: bool) -> RpcResult<Value> {
        self.capability(
            "core.fs.write",
            json!({
                "path": path,
                "content": base64::encode(data),
                "encoding": "base64",
                "mkdir": mkdir,
            }),
        )
    }

    /// 经核心列目录（需 `filesystem:read`），返回 `{path,entries:[{name,dir,size}]}`。
    pub fn fs_list(&self, path: &str) -> RpcResult<Value> {
        self.capability("core.fs.list", json!({ "path": path }))
    }

    /// 经核心删除文件或目录（需 `filesystem:delete`）。
    pub fn fs_delete(&self, path: &str, recursive: bool) -> RpcResult<Value> {
        self.capability(
            "core.fs.delete",
            json!({ "path": path, "recursive": recursive }),
        )
    }

    /// 经核心创建目录（需 `filesystem:write`）。
    pub fn fs_mkdir(&self, path: &str, recursive: bool) -> RpcResult<Value> {
        self.capability(
            "core.fs.mkdir",
            json!({ "path": path, "recursive": recursive }),
        )
    }

    /// 经核心重命名 / 移动文件或目录（需 `filesystem:write`）。
    pub fn fs_move(&self, from: &str, to: &str) -> RpcResult<Value> {
        self.capability("core.fs.move", json!({ "from": from, "to": to }))
    }

    /// 经核心发起 HTTP 请求（需 `network`），返回 `{status,headers,body,truncated}`。
    pub fn net_request(
        &self,
        method: &str,
        url: &str,
        headers: Option<HashMap<String, String>>,
        body: Option<&str>,
    ) -> RpcResult<Value> {
        let mut params = json!({ "method": method, "url": url });
        if let Some(h) = headers {
            params["headers"] = json!(h);
        }
        if let Some(b) = body {
            params["body"] = Value::String(b.to_string());
        }
        self.capability("core.net.request", params)
    }

    /// 经核心执行子进程（需 `process:spawn`），返回 `{exitCode,stdout,stderr,truncated}`。
    pub fn spawn(&self, command: &str, args: &[String]) -> RpcResult<Value> {
        self.capability(
            "core.process.spawn",
            json!({ "command": command, "args": args }),
        )
    }

    /// 请核心用系统默认程序打开文件（需 `shell:open`）。
    pub fn shell_open(&self, path: &str) -> RpcResult<Value> {
        self.capability("core.shell.open", json!({ "path": path }))
    }

    /// 弹系统文件夹选择对话框（需 `shell:pickfolder`），返回 `{path,cancelled}`。
    ///
    /// 这是阻塞调用：对话框不关它就不返回。仅 Windows 支持。
    pub fn pick_folder(&self, initial: &str, title: &str) -> RpcResult<Value> {
        self.capability(
            "core.shell.pickfolder",
            json!({ "initial": initial, "title": title }),
        )
    }

    // -----------------------------------------------------------------------
    // 通道内部实现
    // -----------------------------------------------------------------------

    pub(crate) fn send_message(&self, msg: &Message) -> RpcResult<()> {
        let mut data = serde_json::to_vec(msg)?;
        data.push(b'\n');
        let mut out = lock(&self.inner.out);
        out.write_all(&data)?;
        out.flush()?;
        Ok(())
    }

    fn notify(&self, method: &str, params: Value) -> RpcResult<()> {
        self.send_message(&Message::notification(method, params))
    }

    /// 向核心发起一次请求并等待响应；`timeout` 为 `None` 时使用 24 小时兜底。
    pub(crate) fn call(
        &self,
        method: &str,
        params: Value,
        timeout: Option<Duration>,
    ) -> RpcResult<Value> {
        let id = Value::from(self.inner.next_id.fetch_add(1, Ordering::SeqCst));
        let key = id_key(&id);
        let (tx, rx) = mpsc::channel::<Message>();
        lock(&self.inner.pending).insert(key.clone(), tx);

        if let Err(e) = self.send_message(&Message::request(id, method, params)) {
            lock(&self.inner.pending).remove(&key);
            return Err(e);
        }

        let wait = timeout.unwrap_or(Duration::from_secs(24 * 60 * 60));
        match rx.recv_timeout(wait) {
            Ok(msg) => match msg.error {
                Some(err) => Err(err),
                None => Ok(msg.result.unwrap_or(Value::Null)),
            },
            Err(mpsc::RecvTimeoutError::Timeout) => {
                lock(&self.inner.pending).remove(&key);
                Err(RpcError::internal(format!(
                    "调用 {method} 超时（{} 秒）",
                    wait.as_secs()
                )))
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                lock(&self.inner.pending).remove(&key);
                Err(RpcError::internal(format!("调用 {method} 的响应通道已关闭")))
            }
        }
    }

    /// 把核心返回的响应交给等待它的那次 `call`。
    pub(crate) fn deliver(&self, msg: &Message) {
        let Some(id) = msg.id.as_ref() else { return };
        if let Some(tx) = lock(&self.inner.pending).remove(&id_key(id)) {
            let _ = tx.send(msg.clone());
        }
    }

    /// 取某个事件的本地处理函数快照（含 `"*"` 通配订阅）。
    pub(crate) fn handlers_for(&self, event: &str) -> Vec<EventHandler> {
        let map = lock(&self.inner.handlers);
        let mut out = map.get(event).cloned().unwrap_or_default();
        if event != "*" {
            if let Some(all) = map.get("*") {
                out.extend(all.iter().cloned());
            }
        }
        out
    }
}

fn str_field(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

/// 用消息 id 的 JSON 文本当键：数字与字符串 id 都能稳定对上。
fn id_key(id: &Value) -> String {
    serde_json::to_string(id).unwrap_or_else(|_| "null".to_string())
}
