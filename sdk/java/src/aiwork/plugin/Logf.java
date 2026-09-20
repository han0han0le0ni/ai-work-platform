package aiwork.plugin;

import java.io.PrintStream;
import java.nio.charset.StandardCharsets;

/**
 * 写 stderr。核心会把 stderr 转发到 logs/&lt;pluginId&gt;.log。
 *
 * <p>注意：stdout 属于 JSON-RPC 硬通道，任何情况下都不要往 System.out 打印调试信息。
 */
public final class Logf {

    private static final PrintStream ERR = new PrintStream(System.err, true, StandardCharsets.UTF_8);

    private Logf() {
    }

    public static void log(String level, String message) {
        ERR.println("[" + level.toUpperCase() + "] " + message);
    }

    public static void info(String message) {
        log("info", message);
    }

    public static void warn(String message) {
        log("warn", message);
    }

    public static void error(String message) {
        log("error", message);
    }
}
