// Package jsonrpc 定义核心与插件之间唯一的硬通道协议：JSON-RPC 2.0（JSON Lines）。
//
// 这一层是整个系统的“宪法”：永远不变，不可插件化。
// 核心只认识这一种消息，不认识 AI、LLM、工具、UI。
package jsonrpc

import (
	"encoding/json"
	"fmt"
)

// Version 是协议版本，写入每条消息的 jsonrpc 字段。
const Version = "2.0"

// 标准错误码。属于公开契约，只增不减。
const (
	CodeParseError             = -32700 // 不是合法 JSON
	CodeInvalidRequest         = -32600 // 不是合法 JSON-RPC
	CodeMethodNotFound         = -32601 // 方法未找到
	CodeInvalidParams          = -32602 // 参数非法
	CodeInternalError          = -32603 // 内部错误
	CodeServiceNotFound        = -32000 // 服务未找到
	CodeProviderDead           = -32001 // 服务提供者已死
	CodePermissionDenied       = -32002 // 权限不足
	CodeSandboxUnavailable     = -32003 // 沙箱不可用
	CodeDependencyMissing      = -32004 // 依赖缺失
	CodeAPIVersionIncompatible = -32005 // apiVersion 不兼容
)

// Message 是 JSON-RPC 2.0 的统一消息载体。
//
// 通过字段组合区分三种消息：
//
//	请求: Method != "" 且 ID 非空
//	通知: Method != "" 且 ID 为空
//	响应: Method == ""
//
// Result / Params 使用 json.RawMessage，保证核心可以原样透传，不做任何解释。
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error 是标准的 JSON-RPC 错误对象。
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// NewError 构造一个错误对象。
func NewError(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsRequest 判断是否为请求。
func (m *Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification 判断是否为通知。
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// IsResponse 判断是否为响应。
func (m *Message) IsResponse() bool { return m.Method == "" }

// IDKey 返回用于匹配请求与响应的键。
func (m *Message) IDKey() string { return string(m.ID) }

// Decode 解析一行 JSON。
func Decode(line []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	if m.JSONRPC == "" {
		m.JSONRPC = Version
	}
	return &m, nil
}

// Encode 序列化为可写入硬通道的一行 JSON。
func Encode(m *Message) ([]byte, error) {
	if m.JSONRPC == "" {
		m.JSONRPC = Version
	}
	return json.Marshal(m)
}

// raw 把任意值转换为 json.RawMessage，nil 归一化为 JSON null。
func raw(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	if r, ok := v.(json.RawMessage); ok {
		if len(r) == 0 {
			return json.RawMessage("null")
		}
		return r
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// NewRequest 构造请求消息。
func NewRequest(id json.RawMessage, method string, params any) *Message {
	return &Message{JSONRPC: Version, ID: id, Method: method, Params: raw(params)}
}

// NewNotification 构造通知消息。
func NewNotification(method string, params any) *Message {
	return &Message{JSONRPC: Version, Method: method, Params: raw(params)}
}

// NewResponse 构造成功响应。
func NewResponse(id json.RawMessage, result any) *Message {
	return &Message{JSONRPC: Version, ID: id, Result: raw(result)}
}

// NewErrorResponse 构造错误响应。
func NewErrorResponse(id json.RawMessage, e *Error) *Message {
	if e == nil {
		e = NewError(CodeInternalError, "未知错误")
	}
	return &Message{JSONRPC: Version, ID: id, Error: e}
}
