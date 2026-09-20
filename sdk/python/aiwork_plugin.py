"""AI 工作平台插件 SDK（Python）。

用法：

    from aiwork_plugin import Plugin, RpcError, serve

    class Echo(Plugin):
        def activate(self, ctx):
            self.ctx = ctx
            ctx.provide("echo.v1", {"description": "回显"})

        def handle(self, service, method, args):
            if method == "run":
                return {"echo": (args or {}).get("text", "")}
            raise RpcError(CODE_METHOD_NOT_FOUND, "不支持的方法 " + method)

    if __name__ == "__main__":
        serve(Echo())

硬通道约束（务必记住）：
  * stdin / stdout 属于 JSON-RPC，**只能**输出协议消息
  * 需要打日志请用 ctx.log() 或本模块的 logf()（写 stderr，核心会转发到 logs/<pluginId>.log）
  * 千万不要 print()，那会污染协议
"""

import json
import os
import queue
import sys
import threading

# ---------------------------------------------------------------------------
# 标准错误码（与核心一致，公开契约）
# ---------------------------------------------------------------------------

CODE_PARSE_ERROR = -32700
CODE_INVALID_REQUEST = -32600
CODE_METHOD_NOT_FOUND = -32601
CODE_INVALID_PARAMS = -32602
CODE_INTERNAL_ERROR = -32603
CODE_SERVICE_NOT_FOUND = -32000
CODE_PROVIDER_DEAD = -32001
CODE_PERMISSION_DENIED = -32002
CODE_SANDBOX_UNAVAILABLE = -32003
CODE_DEPENDENCY_MISSING = -32004
CODE_API_VERSION_INCOMPATIBLE = -32005

# 权限名（manifest 的 permissions 里声明后，能力代理才会放行）
PERM_NETWORK = "network"
PERM_FS_READ = "filesystem:read"
PERM_FS_WRITE = "filesystem:write"
PERM_PROCESS_SPAWN = "process:spawn"


class RpcError(Exception):
    """可跨进程传递的错误。handler 里抛出它，核心会把 code 原样带给调用方。"""

    def __init__(self, code, message, data=None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.data = data


def rpc_error(code, message, data=None):
    """rpc_error 与 raise RpcError(...) 等价，便于在表达式里使用。"""
    return RpcError(code, message, data)


def logf(level, message, **meta):
    """写 stderr。核心会把 stderr 转发到 logs/<pluginId>.log。"""
    try:
        extra = " " + json.dumps(meta, ensure_ascii=False) if meta else ""
        sys.stderr.write("[%s] %s%s\n" % (level.upper(), message, extra))
        sys.stderr.flush()
    except Exception:
        pass


# ---------------------------------------------------------------------------
# 插件基类
# ---------------------------------------------------------------------------


class Plugin(object):
    """插件作者继承它，并实现下面三个方法。"""

    def activate(self, ctx):
        """收到 core.init 后调用：在这里注册服务、订阅事件。"""
        pass

    def handle(self, service, method, args):
        """处理一次 core.invoke。args 通常是 dict，也可能为空 dict。"""
        raise RpcError(CODE_METHOD_NOT_FOUND, "%s 不支持方法 %s" % (service, method))

    def deactivate(self):
        """收到 core.shutdown 时调用，可做清理。"""
        pass


# ---------------------------------------------------------------------------
# 上下文：插件与核心交互的唯一入口
# ---------------------------------------------------------------------------


class Context(object):
    """只暴露核心的 6 个基础接口 + 能力代理，不暴露任何核心内部实现。"""

    def __init__(self, client):
        self._client = client
        self.plugin_id = ""
        self.api_version = ""
        self.core_version = ""
        self.config_namespace = ""
        self.permissions = []

    # ① 服务注册与发现 -------------------------------------------------------

    def provide(self, name, meta=None):
        """注册服务，服务名必须形如 <名称>.v<版本>。"""
        return self._client.call("core.provide", {"name": name, "meta": meta}, timeout=10)

    def consume(self, name):
        """发现服务，返回提供者插件 ID；找不到会抛 RpcError。"""
        result = self._client.call("core.consume", {"name": name}, timeout=10) or {}
        return result.get("pluginId")

    # ② 服务调用 -------------------------------------------------------------

    def invoke(self, service, method, args=None):
        """调用另一个插件提供的服务，返回原始结果。"""
        return self._client.call(
            "core.invoke",
            {"service": service, "method": method, "args": args if args is not None else {}},
            timeout=None,
        )

    # ③ 事件总线 -------------------------------------------------------------

    def emit(self, event, payload=None):
        """发布事件，事件名必须形如 <命名空间>.<动作>。"""
        self._client.notify("core.emit", {"event": event, "payload": payload})

    def on(self, event, handler):
        """订阅事件；"*" 表示订阅全部。"""
        self._client.add_handler(event, handler)

    # ④ 配置管理 -------------------------------------------------------------

    def get_config(self, key, default=None):
        """读取配置；键不存在时返回 default。"""
        result = self._client.call("core.getConfig", {"key": key}, timeout=10) or {}
        if not result.get("found"):
            return default
        value = result.get("value")
        return default if value is None else value

    def set_config(self, key, value):
        """写入配置。配置键只增不减。"""
        return self._client.call("core.setConfig", {"key": key, "value": value}, timeout=10)

    # ⑤ 日志 ----------------------------------------------------------------

    def log(self, level, message, meta=None, **extra):
        """上报日志：核心写入 logs/<pluginId>.log，并广播 core.log 事件。

        两种写法都支持：
            ctx.log("info", "已就绪", {"count": 3})
            ctx.log("info", "已就绪", count=3)
        """
        if extra:
            merged = dict(meta or {})
            merged.update(extra)
            meta = merged
        try:
            self._client.notify("core.log", {"level": level, "msg": message, "meta": meta})
        except Exception as exc:  # 日志失败绝不能拖垮插件
            logf("warn", "上报日志失败: %s" % exc)

    # ⑥ 沙箱（接口预留） -----------------------------------------------------

    def sandbox_run(self, code, options=None):
        """请求沙箱执行代码；未安装沙箱插件时抛 RpcError(-32003)。"""
        return self._client.call("core.sandbox.run", {"code": code, "options": options}, timeout=None)

    # 能力代理（规范 3.5）：文件 / 网络 / 子进程由核心代为执行 ----------------

    def capability(self, method, params):
        """调用能力代理的任意方法。权限未在 manifest 声明时核心返回 -32002。"""
        return self._client.call(method, params, timeout=None)

    def fs_read(self, path, limit=None):
        params = {"path": path}
        if limit:
            params["limit"] = limit
        return self.capability("core.fs.read", params)

    def fs_write(self, path, content, append=False, mkdir=False):
        return self.capability(
            "core.fs.write",
            {"path": path, "content": content, "append": append, "mkdir": mkdir},
        )

    def fs_list(self, path="."):
        return self.capability("core.fs.list", {"path": path})

    def net_request(self, method, url, headers=None, body=""):
        return self.capability(
            "core.net.request",
            {"method": method, "url": url, "headers": headers or {}, "body": body},
        )

    def spawn(self, command, args=None):
        return self.capability("core.process.spawn", {"command": command, "args": args or []})


# ---------------------------------------------------------------------------
# 内部：JSON-RPC 客户端
# ---------------------------------------------------------------------------


class _Slot(object):
    """一次出站调用的等待槽。"""

    __slots__ = ("event", "message")

    def __init__(self):
        self.event = threading.Event()
        self.message = None


class _Client(object):
    def __init__(self):
        self._write_lock = threading.Lock()
        self._pending = {}
        self._pending_lock = threading.Lock()
        self._handlers = {}
        self._handlers_lock = threading.Lock()
        self._next_id = 0
        self._id_lock = threading.Lock()
        self._inbox = queue.Queue()
        self._stop = threading.Event()
        self.plugin = None
        self.ctx = None

    # --- 收发 --------------------------------------------------------------

    def _send(self, message):
        data = json.dumps(message, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        with self._write_lock:
            sys.stdout.buffer.write(data + b"\n")
            sys.stdout.buffer.flush()

    def _respond(self, rid, result):
        self._send({"jsonrpc": "2.0", "id": rid, "result": result})

    def _respond_error(self, rid, code, message, data=None):
        err = {"code": code, "message": message}
        if data is not None:
            err["data"] = data
        self._send({"jsonrpc": "2.0", "id": rid, "error": err})

    def notify(self, method, params):
        self._send({"jsonrpc": "2.0", "method": method, "params": params})

    def call(self, method, params, timeout=None):
        with self._id_lock:
            self._next_id += 1
            rid = self._next_id
        slot = _Slot()
        with self._pending_lock:
            self._pending[str(rid)] = slot
        try:
            self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
            if not slot.event.wait(timeout):
                raise RpcError(CODE_INTERNAL_ERROR, "调用 %s 超时" % method)
            message = slot.message
            error = message.get("error")
            if error:
                raise RpcError(
                    error.get("code", CODE_INTERNAL_ERROR),
                    error.get("message", "未知错误"),
                    error.get("data"),
                )
            return message.get("result")
        finally:
            with self._pending_lock:
                self._pending.pop(str(rid), None)

    # --- 事件订阅 ----------------------------------------------------------

    def add_handler(self, event, handler):
        with self._handlers_lock:
            self._handlers.setdefault(event, []).append(handler)

    def _handlers_for(self, event):
        with self._handlers_lock:
            return list(self._handlers.get(event, [])) + list(self._handlers.get("*", []))

    # --- 读线程：只负责读，绝不做业务，否则出站调用会死锁 -------------------

    def _reader_loop(self):
        stdin = sys.stdin.buffer
        while True:
            try:
                line = stdin.readline()
            except Exception as exc:
                logf("error", "读取 stdin 失败: %s" % exc)
                break
            if not line:
                break
            if line.startswith(b"\xef\xbb\xbf"):  # 容忍意外混入的 BOM
                line = line[3:]
            line = line.strip()
            if not line:
                continue
            try:
                message = json.loads(line.decode("utf-8"))
            except Exception as exc:
                logf("error", "无法解析来自核心的消息: %s" % exc)
                continue

            if message.get("method") is None:
                self._deliver(message)  # 是响应
            else:
                self._inbox.put(message)

        # stdin 关闭（核心退出）：通知工作线程收尾
        self._inbox.put(None)
        self._stop.set()

    def _deliver(self, message):
        key = str(message.get("id"))
        with self._pending_lock:
            slot = self._pending.get(key)
        if slot is not None:
            slot.message = message
            slot.event.set()

    # --- 工作线程：串行取消息，请求各自开线程处理 -------------------------

    def _worker_loop(self):
        while True:
            try:
                message = self._inbox.get(timeout=0.2)
            except queue.Empty:
                if self._stop.is_set():
                    return
                continue
            if message is None:
                return

            method = message.get("method")
            if method == "core.shutdown":
                self._handle_shutdown()
                return
            if message.get("id") is not None:
                # 每个请求单独一个线程，长耗时处理不会堵住其他请求
                threading.Thread(target=self._handle_request, args=(message,), daemon=True).start()
            else:
                self._handle_notification(message)

    def _handle_request(self, message):
        rid = message.get("id")
        method = message.get("method")
        params = message.get("params") or {}

        if method == "core.init":
            self.ctx.plugin_id = params.get("pluginId", "")
            self.ctx.api_version = str(params.get("apiVersion", ""))
            self.ctx.core_version = str(params.get("coreVersion", ""))
            self.ctx.config_namespace = params.get("configNamespace") or self.ctx.plugin_id
            self.ctx.permissions = list(params.get("permissions") or [])
            try:
                self.plugin.activate(self.ctx)
            except RpcError as exc:
                self._respond_error(rid, exc.code, exc.message, exc.data)
                return
            except Exception as exc:
                self._respond_error(rid, CODE_INTERNAL_ERROR, "activate 失败: %s" % exc)
                return
            self._respond(rid, {"ready": True})
            return

        if method == "core.invoke":
            service = params.get("service", "")
            method_name = params.get("method", "")
            args = params.get("args")
            if not isinstance(args, dict):
                args = {}
            try:
                result = self.plugin.handle(service, method_name, args)
            except RpcError as exc:
                self._respond_error(rid, exc.code, exc.message, exc.data)
                return
            except Exception as exc:
                logf("error", "处理 %s.%s 时异常: %s" % (service, method_name, exc))
                self._respond_error(rid, CODE_INTERNAL_ERROR, "插件内部错误: %s" % exc)
                return
            self._respond(rid, result)
            return

        self._respond_error(rid, CODE_METHOD_NOT_FOUND, "插件不支持方法 %s" % method)

    def _handle_notification(self, message):
        method = message.get("method")
        if method != "core.event":
            logf("warn", "收到未知通知 %s" % method)
            return
        params = message.get("params") or {}
        event = params.get("event", "")
        payload = params.get("payload")
        for handler in self._handlers_for(event):
            try:
                handler(payload)
            except Exception as exc:
                logf("error", "处理事件 %s 时异常: %s" % (event, exc))

    def _handle_shutdown(self):
        try:
            self.plugin.deactivate()
        except Exception as exc:
            logf("error", "deactivate 失败: %s" % exc)
        try:
            sys.stdout.buffer.flush()
        except Exception:
            pass
        os._exit(0)


# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------


def serve(plugin):
    """阻塞运行插件主循环，直到 stdin 关闭或核心要求退出。"""
    for stream in (sys.stdin, sys.stdout):
        try:
            stream.reconfigure(encoding="utf-8")
        except Exception:
            pass

    client = _Client()
    client.plugin = plugin
    client.ctx = Context(client)

    threading.Thread(target=client._reader_loop, daemon=True).start()
    threading.Thread(target=client._worker_loop, daemon=True).start()

    try:
        while not client._stop.wait(0.2):
            pass
    except KeyboardInterrupt:
        try:
            plugin.deactivate()
        except Exception:
            pass
    return 0


__all__ = [
    "Plugin",
    "Context",
    "RpcError",
    "rpc_error",
    "logf",
    "serve",
    "CODE_PARSE_ERROR",
    "CODE_INVALID_REQUEST",
    "CODE_METHOD_NOT_FOUND",
    "CODE_INVALID_PARAMS",
    "CODE_INTERNAL_ERROR",
    "CODE_SERVICE_NOT_FOUND",
    "CODE_PROVIDER_DEAD",
    "CODE_PERMISSION_DENIED",
    "CODE_SANDBOX_UNAVAILABLE",
    "CODE_DEPENDENCY_MISSING",
    "CODE_API_VERSION_INCOMPATIBLE",
    "PERM_NETWORK",
    "PERM_FS_READ",
    "PERM_FS_WRITE",
    "PERM_PROCESS_SPAWN",
]
