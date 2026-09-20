package aiwork.plugin;

/** 插件入口。在 main 里调 {@code Serve.run(new YourPlugin())} 即可。 */
public final class Serve {

    private Serve() {
    }

    /** 阻塞运行插件主循环，直到 stdin 关闭或核心要求退出。 */
    public static void run(Plugin plugin) {
        Client client = new Client();
        client.attach(plugin);

        Thread reader = new Thread(client::readerLoop, "aiwork-reader");
        reader.setDaemon(true);
        reader.start();

        // 主线程做派发，天然阻塞
        client.dispatchLoop();
    }
}
