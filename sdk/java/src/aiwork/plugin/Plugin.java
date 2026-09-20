package aiwork.plugin;

import java.util.Map;

/**
 * 插件作者实现这个接口。
 *
 * <p>用法：
 * <pre>
 * public final class Echo implements Plugin {
 *     private Context ctx;
 *
 *     public void activate(Context ctx) {
 *         this.ctx = ctx;
 *         ctx.provide("echo.v1", Json.map("description", "回显"));
 *     }
 *
 *     public Object handle(String service, String method, Map&lt;String, Object&gt; args) {
 *         if ("run".equals(method)) {
 *             return Json.map("echo", args.get("text"));
 *         }
 *         throw new RpcError(RpcError.METHOD_NOT_FOUND, service + " 不支持方法 " + method);
 *     }
 *
 *     public static void main(String[] args) {
 *         Serve.run(new Echo());
 *     }
 * }
 * </pre>
 */
public interface Plugin {

    /** 收到 core.init 后调用：在这里注册服务、订阅事件。 */
    void activate(Context ctx) throws Exception;

    /** 处理一次 core.invoke。args 一定不是 null，可能是空 Map。 */
    Object handle(String service, String method, Map<String, Object> args) throws Exception;

    /** 收到 core.shutdown 时调用，可做清理。默认什么都不做。 */
    default void deactivate() throws Exception {
    }
}
