package aiwork.plugin;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;

/**
 * 上下文：插件与核心交互的唯一入口。
 *
 * <p>只暴露核心的 6 个基础接口 + 能力代理，不暴露任何核心内部实现。
 */
public final class Context {

    private final Client client;

    /** 核心在 core.init 里告知的插件 ID。 */
    public String pluginId = "";
    /** 插件协议版本。 */
    public String apiVersion = "";
    /** 核心版本。 */
    public String coreVersion = "";
    /** 配置命名空间，等同插件 ID。 */
    public String configNamespace = "";
    /** manifest 里声明的权限；核心已按此授权给能力代理。 */
    public final List<String> permissions = new ArrayList<>();

    Context(Client client) {
        this.client = client;
    }

    // ------------------------------------------------------------------
    // ① 服务注册与发现
    // ------------------------------------------------------------------

    /** 注册服务，服务名必须形如 &lt;名称&gt;.v&lt;版本&gt;。 */
    public void provide(String name, Object meta) {
        client.call("core.provide", Json.map("name", name, "meta", meta), 10_000);
    }

    public void provide(String name) {
        provide(name, null);
    }

    /** 发现服务，返回提供者插件 ID；找不到会抛 {@link RpcError}。 */
    public String consume(String name) {
        Object result = client.call("core.consume", Json.map("name", name), 10_000);
        return Json.asString(Json.asObject(result).get("pluginId"), null);
    }

    // ------------------------------------------------------------------
    // ② 服务调用
    // ------------------------------------------------------------------

    /** 调用另一个插件提供的服务，返回原始结果。 */
    public Object invoke(String service, String method, Object args) {
        return client.call(
                "core.invoke",
                Json.map("service", service, "method", method, "args", args == null ? Json.map() : args),
                0);
    }

    // ------------------------------------------------------------------
    // ③ 事件总线
    // ------------------------------------------------------------------

    /** 发布事件，事件名必须形如 &lt;命名空间&gt;.&lt;动作&gt;。 */
    public void emit(String event, Object payload) {
        client.notify("core.emit", Json.map("event", event, "payload", payload));
    }

    /** 订阅事件；"*" 表示订阅全部。 */
    public void on(String event, Consumer<Object> handler) {
        client.addHandler(event, handler);
    }

    // ------------------------------------------------------------------
    // ④ 配置管理
    // ------------------------------------------------------------------

    /** 读取配置；键不存在时返回 fallback。 */
    public Object getConfig(String key, Object fallback) {
        Map<String, Object> result = Json.asObject(client.call("core.getConfig", Json.map("key", key), 10_000));
        if (!Json.asBool(result.get("found"), false)) {
            return fallback;
        }
        Object value = result.get("value");
        return value == null ? fallback : value;
    }

    public String getConfigString(String key, String fallback) {
        return Json.asString(getConfig(key, fallback), fallback);
    }

    public int getConfigInt(String key, int fallback) {
        return Json.asInt(getConfig(key, fallback), fallback);
    }

    /** 写入配置。配置键只增不减。 */
    public void setConfig(String key, Object value) {
        client.call("core.setConfig", Json.map("key", key, "value", value), 10_000);
    }

    // ------------------------------------------------------------------
    // ⑤ 日志
    // ------------------------------------------------------------------

    public void log(String level, String message) {
        log(level, message, null);
    }

    /** 上报日志：核心写入 logs/&lt;pluginId&gt;.log，并广播 core.log 事件。 */
    public void log(String level, String message, Object meta) {
        try {
            client.notify("core.log", Json.map("level", level, "msg", message, "meta", meta));
        } catch (RuntimeException e) {
            Logf.warn("上报日志失败: " + e.getMessage());
        }
    }

    // ------------------------------------------------------------------
    // ⑥ 沙箱（接口预留）
    // ------------------------------------------------------------------

    /** 请求沙箱执行代码；未安装沙箱插件时抛 {@link RpcError}(-32003)。 */
    public Object sandboxRun(String code, Object options) {
        return client.call("core.sandbox.run", Json.map("code", code, "options", options), 0);
    }

    // ------------------------------------------------------------------
    // 能力代理（规范 3.5）：文件 / 网络 / 子进程由核心代为执行
    // 权限必须在 manifest 的 permissions 里声明，否则核心返回 -32002
    // ------------------------------------------------------------------

    public Object capability(String method, Object params) {
        return client.call(method, params, 0);
    }

    public Object fsRead(String path) {
        return capability("core.fs.read", Json.map("path", path));
    }

    public Object fsWrite(String path, String content, boolean append, boolean mkdir) {
        return capability("core.fs.write",
                Json.map("path", path, "content", content, "append", append, "mkdir", mkdir));
    }

    public Object fsList(String path) {
        return capability("core.fs.list", Json.map("path", path));
    }

    public Object netRequest(String method, String url, Map<String, Object> headers, String body) {
        return capability("core.net.request",
                Json.map("method", method, "url", url, "headers", headers, "body", body));
    }

    public Object spawn(String command, List<String> args) {
        return capability("core.process.spawn", Json.map("command", command, "args", args));
    }
}
