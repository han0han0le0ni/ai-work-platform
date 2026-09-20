package aiwork.plugin;

/**
 * 可跨进程传递的 JSON-RPC 错误。
 *
 * <p>在 {@link Plugin#handle} 里抛出它，核心会把 code / message / data 原样带给调用方。
 */
public class RpcError extends RuntimeException {

    private static final long serialVersionUID = 1L;

    // 标准错误码（与核心一致，公开契约）
    public static final int PARSE_ERROR = -32700;
    public static final int INVALID_REQUEST = -32600;
    public static final int METHOD_NOT_FOUND = -32601;
    public static final int INVALID_PARAMS = -32602;
    public static final int INTERNAL_ERROR = -32603;
    public static final int SERVICE_NOT_FOUND = -32000;
    public static final int PROVIDER_DEAD = -32001;
    public static final int PERMISSION_DENIED = -32002;
    public static final int SANDBOX_UNAVAILABLE = -32003;
    public static final int DEPENDENCY_MISSING = -32004;
    public static final int API_VERSION_INCOMPATIBLE = -32005;

    private final int code;
    private final Object data;

    public RpcError(int code, String message) {
        this(code, message, null);
    }

    public RpcError(int code, String message, Object data) {
        super(message);
        this.code = code;
        this.data = data;
    }

    public int getCode() {
        return code;
    }

    public Object getData() {
        return data;
    }
}
