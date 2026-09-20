// Package plugin 是 Go 插件作者使用的 SDK。
//
// 它把核心的 6 个基础接口翻译成简单的方法调用，并把插件能力暴露为服务。
// 插件进程的 stdout 只允许输出 JSON-RPC；所有日志请走 Logf/stderr。
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
)

const rpcVersion = "2.0"

// 标准错误码（与核心保持一致，公开契约）。完整表见规范 2.5。
const (
	CodeParseError             = -32700
	CodeInvalidRequest         = -32600
	CodeMethodNotFound         = -32601
	CodeInvalidParams          = -32602
	CodeInternalError          = -32603
	CodeServiceNotFound        = -32000
	CodeProviderDead           = -32001
	CodePermissionDenied       = -32002
	CodeSandboxUnavailable     = -32003
	CodeDependencyMissing      = -32004
	CodeAPIVersionIncompatible = -32005
)

// RPCError 是 JSON-RPC 错误对象，也是插件向核心回传错误的唯一载体。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Errorf 构造一个可跨进程传递的错误。
func Errorf(code int, format string, args ...any) error {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf 提取错误码，非 RPC 错误返回 CodeInternalError。
func CodeOf(err error) int {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternalError
}

// IsCode 判断错误码是否为指定值。
func IsCode(err error, code int) bool {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}

func asRPCError(err error) *RPCError {
	var e *RPCError
	if errors.As(err, &e) {
		return e
	}
	return &RPCError{Code: CodeInternalError, Message: err.Error()}
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m *message) isRequest() bool { return m.Method != "" && len(m.ID) > 0 }

func rawParams(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(v)
}
