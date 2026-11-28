//! JSON-RPC 2.0 的错误对象与线缆消息。

use std::fmt;

use serde::{Deserialize, Serialize};
use serde_json::Value;

// 标准错误码（与核心保持一致，公开契约）。完整表见规范 2.5。
pub const CODE_PARSE_ERROR: i64 = -32700;
pub const CODE_INVALID_REQUEST: i64 = -32600;
pub const CODE_METHOD_NOT_FOUND: i64 = -32601;
pub const CODE_INVALID_PARAMS: i64 = -32602;
pub const CODE_INTERNAL_ERROR: i64 = -32603;
pub const CODE_SERVICE_NOT_FOUND: i64 = -32000;
pub const CODE_PROVIDER_DEAD: i64 = -32001;
pub const CODE_PERMISSION_DENIED: i64 = -32002;
pub const CODE_SANDBOX_UNAVAILABLE: i64 = -32003;
pub const CODE_DEPENDENCY_MISSING: i64 = -32004;
pub const CODE_API_VERSION_INCOMPATIBLE: i64 = -32005;

/// 插件向核心回传错误的唯一载体。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RpcError {
    pub code: i64,
    pub message: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl RpcError {
    pub fn new(code: i64, message: impl Into<String>) -> Self {
        Self { code, message: message.into(), data: None }
    }

    /// 附带一段结构化数据（会原样出现在 `error.data` 里）。
    pub fn with_data(mut self, data: Value) -> Self {
        self.data = Some(data);
        self
    }

    pub fn internal(message: impl Into<String>) -> Self {
        Self::new(CODE_INTERNAL_ERROR, message)
    }

    pub fn method_not_found(message: impl Into<String>) -> Self {
        Self::new(CODE_METHOD_NOT_FOUND, message)
    }

    pub fn service_not_found(message: impl Into<String>) -> Self {
        Self::new(CODE_SERVICE_NOT_FOUND, message)
    }

    pub fn permission_denied(message: impl Into<String>) -> Self {
        Self::new(CODE_PERMISSION_DENIED, message)
    }

    pub fn invalid_params(message: impl Into<String>) -> Self {
        Self::new(CODE_INVALID_PARAMS, message)
    }
}

impl fmt::Display for RpcError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "[{}] {}", self.code, self.message)
    }
}

impl std::error::Error for RpcError {}

impl From<serde_json::Error> for RpcError {
    fn from(e: serde_json::Error) -> Self {
        Self::internal(format!("JSON 序列化失败: {e}"))
    }
}

impl From<std::io::Error> for RpcError {
    fn from(e: std::io::Error) -> Self {
        Self::internal(format!("I/O 失败: {e}"))
    }
}

/// 插件方法的统一返回类型。
pub type RpcResult<T> = Result<T, RpcError>;

/// 线缆上的一行 JSON-RPC 消息。
///
/// 请求、响应、通知共用这一个结构：`method` 为空表示响应，`id` 为空表示通知。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub(crate) struct Message {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jsonrpc: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub id: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub method: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub params: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<RpcError>,
}

impl Message {
    fn base() -> Self {
        Self { jsonrpc: Some("2.0".to_string()), ..Default::default() }
    }

    pub(crate) fn request(id: Value, method: &str, params: Value) -> Self {
        Self { id: Some(id), method: Some(method.to_string()), params: Some(params), ..Self::base() }
    }

    pub(crate) fn notification(method: &str, params: Value) -> Self {
        Self { method: Some(method.to_string()), params: Some(params), ..Self::base() }
    }

    pub(crate) fn response(id: Value, result: Value) -> Self {
        Self { id: Some(id), result: Some(result), ..Self::base() }
    }

    pub(crate) fn error_response(id: Value, error: RpcError) -> Self {
        Self { id: Some(id), error: Some(error), ..Self::base() }
    }
}
