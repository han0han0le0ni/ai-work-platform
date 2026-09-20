package aiwork.plugin;

import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStreamWriter;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import java.util.function.Consumer;

/**
 * JSON-RPC 客户端：读线程只负责收，派发线程负责分发，请求各自在独立线程里处理。
 *
 * <p>这样拆开是必须的——handler 里可能会 {@code ctx.invoke} 去调用别的插件，
 * 如果读和派发在同一个线程上，那个响应永远读不到，直接死锁。
 */
final class Client {

    /** 读线程在 stdin 关闭时投递的结束信号。 */
    private static final Map<String, Object> STOP = Json.map("__stop", true);

    private final Object writeLock = new Object();
    private final BufferedWriter out;
    private final BufferedReader in;

    private final Map<String, Slot> pending = new ConcurrentHashMap<>();
    private final BlockingQueue<Map<String, Object>> inbox = new LinkedBlockingQueue<>();
    private final Map<String, List<Consumer<Object>>> handlers = new ConcurrentHashMap<>();
    private final ExecutorService workers = Executors.newCachedThreadPool(runnable -> {
        Thread thread = new Thread(runnable, "aiwork-handler");
        thread.setDaemon(true);
        return thread;
    });
    private final AtomicLong nextId = new AtomicLong();
    private volatile boolean stopped;

    private Plugin plugin;
    private Context ctx;

    Client() {
        this.out = new BufferedWriter(new OutputStreamWriter(System.out, StandardCharsets.UTF_8));
        this.in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
    }

    void attach(Plugin plugin) {
        this.plugin = plugin;
        this.ctx = new Context(this);
    }

    Context context() {
        return ctx;
    }

    // ------------------------------------------------------------------
    // 出站
    // ------------------------------------------------------------------

    void send(Object message) {
        String line = Json.write(message);
        synchronized (writeLock) {
            try {
                out.write(line);
                out.write('\n');
                out.flush();
            } catch (IOException e) {
                throw new RpcError(RpcError.INTERNAL_ERROR, "写入 stdout 失败: " + e.getMessage());
            }
        }
    }

    void notify(String method, Object params) {
        send(Json.map("jsonrpc", "2.0", "method", method, "params", params));
    }

    /**
     * 发起一次调用并等待响应。
     *
     * @param timeoutMs &lt;= 0 表示一直等（直到核心回应）
     * @return 响应里的 result
     */
    Object call(String method, Object params, long timeoutMs) {
        long id = nextId.incrementAndGet();
        Slot slot = new Slot();
        pending.put(String.valueOf(id), slot);
        try {
            send(Json.map("jsonrpc", "2.0", "id", id, "method", method, "params", params));
            boolean ready;
            try {
                if (timeoutMs <= 0) {
                    slot.latch.await();
                    ready = true;
                } else {
                    ready = slot.latch.await(timeoutMs, TimeUnit.MILLISECONDS);
                }
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new RpcError(RpcError.INTERNAL_ERROR, "调用 " + method + " 被中断");
            }
            if (!ready) {
                throw new RpcError(RpcError.INTERNAL_ERROR, "调用 " + method + " 超时");
            }
            Map<String, Object> message = slot.message;
            Object error = message.get("error");
            if (error != null) {
                Map<String, Object> e = Json.asObject(error);
                throw new RpcError(
                        Json.asInt(e.get("code"), RpcError.INTERNAL_ERROR),
                        Json.asString(e.get("message"), "未知错误"),
                        e.get("data"));
            }
            return message.get("result");
        } finally {
            pending.remove(String.valueOf(id));
        }
    }

    void addHandler(String event, Consumer<Object> handler) {
        handlers.computeIfAbsent(event, k -> new java.util.concurrent.CopyOnWriteArrayList<>()).add(handler);
    }

    // ------------------------------------------------------------------
    // 读线程
    // ------------------------------------------------------------------

    void readerLoop() {
        try {
            String line;
            while ((line = in.readLine()) != null) {
                if (!line.isEmpty() && line.charAt(0) == '\uFEFF') {
                    line = line.substring(1);
                }
                line = line.trim();
                if (line.isEmpty()) {
                    continue;
                }
                Map<String, Object> message;
                try {
                    message = Json.asObject(Json.parse(line));
                } catch (RuntimeException e) {
                    Logf.error("无法解析来自核心的消息: " + e.getMessage());
                    continue;
                }
                if (message.get("method") == null) {
                    deliver(message);
                } else {
                    inbox.put(message);
                }
            }
        } catch (IOException e) {
            Logf.error("读取 stdin 失败: " + e.getMessage());
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } finally {
            stopped = true;
            inbox.offer(STOP);
        }
    }

    private void deliver(Map<String, Object> message) {
        Slot slot = pending.get(String.valueOf(message.get("id")));
        if (slot != null) {
            slot.message = message;
            slot.latch.countDown();
        }
    }

    // ------------------------------------------------------------------
    // 派发循环（跑在主线程）
    // ------------------------------------------------------------------

    void dispatchLoop() {
        while (true) {
            Map<String, Object> message;
            try {
                message = inbox.poll(200, TimeUnit.MILLISECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                return;
            }
            if (message == null) {
                if (stopped) {
                    return;
                }
                continue;
            }
            if (message == STOP) {
                return;
            }

            String method = Json.asString(message.get("method"), "");
            if ("core.shutdown".equals(method)) {
                handleShutdown();
                return;
            }
            if (message.get("id") != null) {
                workers.execute(() -> handleRequest(message));
            } else {
                handleNotification(message);
            }
        }
    }

    private void handleRequest(Map<String, Object> message) {
        Object id = message.get("id");
        String method = Json.asString(message.get("method"), "");
        Map<String, Object> params = Json.asObject(message.get("params"));

        if ("core.init".equals(method)) {
            ctx.pluginId = Json.asString(params.get("pluginId"), "");
            ctx.apiVersion = String.valueOf(params.getOrDefault("apiVersion", ""));
            ctx.coreVersion = String.valueOf(params.getOrDefault("coreVersion", ""));
            ctx.configNamespace = Json.asString(params.get("configNamespace"), ctx.pluginId);
            ctx.permissions.clear();
            for (Object perm : Json.asArray(params.get("permissions"))) {
                ctx.permissions.add(String.valueOf(perm));
            }
            try {
                plugin.activate(ctx);
            } catch (RpcError e) {
                respondError(id, e.getCode(), e.getMessage(), e.getData());
                return;
            } catch (Exception e) {
                respondError(id, RpcError.INTERNAL_ERROR, "activate 失败: " + e);
                return;
            }
            respond(id, Json.map("ready", true));
            return;
        }

        if ("core.invoke".equals(method)) {
            String service = Json.asString(params.get("service"), "");
            String name = Json.asString(params.get("method"), "");
            Map<String, Object> args = Json.asObject(params.get("args"));
            Object result;
            try {
                result = plugin.handle(service, name, args);
            } catch (RpcError e) {
                respondError(id, e.getCode(), e.getMessage(), e.getData());
                return;
            } catch (Exception e) {
                Logf.error("处理 " + service + "." + name + " 时异常: " + e);
                respondError(id, RpcError.INTERNAL_ERROR, "插件内部错误: " + e);
                return;
            }
            respond(id, result);
            return;
        }

        respondError(id, RpcError.METHOD_NOT_FOUND, "插件不支持方法 " + method);
    }

    private void handleNotification(Map<String, Object> message) {
        String method = Json.asString(message.get("method"), "");
        if (!"core.event".equals(method)) {
            Logf.warn("收到未知通知 " + method);
            return;
        }
        Map<String, Object> params = Json.asObject(message.get("params"));
        String event = Json.asString(params.get("event"), "");
        Object payload = params.get("payload");

        List<Consumer<Object>> targets = new ArrayList<>();
        List<Consumer<Object>> direct = handlers.get(event);
        if (direct != null) {
            targets.addAll(direct);
        }
        List<Consumer<Object>> wildcard = handlers.get("*");
        if (wildcard != null) {
            targets.addAll(wildcard);
        }
        for (Consumer<Object> handler : targets) {
            try {
                handler.accept(payload);
            } catch (RuntimeException e) {
                Logf.error("处理事件 " + event + " 时异常: " + e);
            }
        }
    }

    private void respond(Object id, Object result) {
        send(Json.map("jsonrpc", "2.0", "id", id, "result", result));
    }

    private void respondError(Object id, int code, String message) {
        respondError(id, code, message, null);
    }

    private void respondError(Object id, int code, String message, Object data) {
        Map<String, Object> error = Json.map("code", code, "message", message);
        if (data != null) {
            error.put("data", data);
        }
        send(Json.map("jsonrpc", "2.0", "id", id, "error", error));
    }

    private void handleShutdown() {
        try {
            plugin.deactivate();
        } catch (Exception e) {
            Logf.error("deactivate 失败: " + e);
        }
        synchronized (writeLock) {
            try {
                out.flush();
            } catch (IOException ignored) {
                // 已经要退出了，忽略
            }
        }
        System.exit(0);
    }

    private static final class Slot {
        final CountDownLatch latch = new CountDownLatch(1);
        volatile Map<String, Object> message;
    }
}
