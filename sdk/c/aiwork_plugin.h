/* AI 工作平台插件 SDK（C99）
 *
 * 用法：把 aiwork_plugin.h / aiwork_plugin.c 一起编进你的插件即可，
 * 不依赖任何第三方库。
 *
 *   #include "aiwork_plugin.h"
 *
 *   static void on_activate(aw_ctx *ctx) {
 *       aw_provide(ctx, "echoC.v1", NULL);
 *   }
 *
 *   static aj_value *on_handle(aw_ctx *ctx, const char *service,
 *                              const char *method, const aj_value *args) {
 *       if (strcmp(method, "run") != 0) {
 *           aw_fail(ctx, AW_METHOD_NOT_FOUND, "不支持的方法");
 *           return NULL;
 *       }
 *       aj_value *out = aj_obj();
 *       aj_obj_set(out, "echo", aj_str(aj_get_str(args, "text", "")));
 *       return out;   // SDK 负责释放
 *   }
 *
 *   int main(void) {
 *       aw_plugin plugin = { on_activate, on_handle, NULL };
 *       return aw_serve(&plugin);
 *   }
 *
 * 硬通道约束：stdin / stdout 属于 JSON-RPC，只能输出协议消息。
 * 需要打日志请用 aw_log() / aw_logf()（走 stderr）。
 *
 * 线程模型：单线程、可重入。aw_invoke() 在 handler 里调用时会就地“泵”消息，
 * 期间到达的其他请求会被递归处理。请不要在 handler 里做长阻塞。
 */

#ifndef AIWORK_PLUGIN_H
#define AIWORK_PLUGIN_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ------------------------------------------------------------------ */
/* 标准错误码（与核心一致，公开契约）                                   */
/* ------------------------------------------------------------------ */

#define AW_PARSE_ERROR              (-32700)
#define AW_INVALID_REQUEST          (-32600)
#define AW_METHOD_NOT_FOUND         (-32601)
#define AW_INVALID_PARAMS           (-32602)
#define AW_INTERNAL_ERROR           (-32603)
#define AW_SERVICE_NOT_FOUND        (-32000)
#define AW_PROVIDER_DEAD            (-32001)
#define AW_PERMISSION_DENIED        (-32002)
#define AW_SANDBOX_UNAVAILABLE      (-32003)
#define AW_DEPENDENCY_MISSING       (-32004)
#define AW_API_VERSION_INCOMPATIBLE (-32005)

/* ------------------------------------------------------------------ */
/* 极简 JSON DOM                                                       */
/* ------------------------------------------------------------------ */

typedef enum {
    AJ_NULL = 0,
    AJ_BOOL,
    AJ_NUM,
    AJ_STR,
    AJ_ARR,
    AJ_OBJ
} aj_type;

typedef struct aj_value aj_value;

/* --- 解析与序列化 --- */
aj_value *aj_parse(const char *text);
aj_value *aj_parsen(const char *text, size_t len);
void      aj_free(aj_value *value);          /* 传 NULL 安全 */
char     *aj_write(const aj_value *value);   /* 返回 malloc 的字符串，调用方 free */

/* --- 构造 --- */
aj_value *aj_null(void);
aj_value *aj_bool(int value);
aj_value *aj_num(double value);
aj_value *aj_str(const char *value);         /* value 为 NULL 时等价于空串 */
aj_value *aj_arr(void);
aj_value *aj_obj(void);

void aj_arr_push(aj_value *array, aj_value *item);            /* 接管 item 所有权 */
void aj_obj_set(aj_value *object, const char *key, aj_value *value); /* 接管 value 所有权 */

/* --- 访问 --- */
aj_type         aj_type_of(const aj_value *value);
const aj_value *aj_get(const aj_value *object, const char *key);   /* 无此键返回 NULL */
const char     *aj_get_str(const aj_value *object, const char *key, const char *fallback);
double          aj_get_num(const aj_value *object, const char *key, double fallback);
int             aj_get_bool(const aj_value *object, const char *key, int fallback);
const char     *aj_as_str(const aj_value *value, const char *fallback);
double          aj_as_num(const aj_value *value, double fallback);
int             aj_as_bool(const aj_value *value, int fallback);
size_t          aj_count(const aj_value *value);                    /* 数组/对象元素个数 */
const aj_value *aj_at(const aj_value *array, size_t index);
const char     *aj_key_at(const aj_value *object, size_t index);

/* ------------------------------------------------------------------ */
/* 插件协议                                                            */
/* ------------------------------------------------------------------ */

typedef struct aw_ctx aw_ctx;

typedef void       (*aw_activate_fn)(aw_ctx *ctx);
typedef aj_value  *(*aw_handle_fn)(aw_ctx *ctx, const char *service,
                                   const char *method, const aj_value *args);
typedef void       (*aw_deactivate_fn)(aw_ctx *ctx);
typedef void       (*aw_event_fn)(aw_ctx *ctx, const aj_value *payload);

typedef struct {
    aw_activate_fn   activate;    /* 收到 core.init 后调用，可为 NULL */
    aw_handle_fn     handle;      /* 处理 core.invoke，可为 NULL */
    aw_deactivate_fn deactivate;  /* 收到 core.shutdown 时调用，可为 NULL */
} aw_plugin;

/* 阻塞运行插件主循环，直到 stdin 关闭或核心要求退出。返回进程退出码。 */
int aw_serve(const aw_plugin *plugin);

/* --- 上下文：与核心交互的唯一入口 --- */

/* ① 服务注册与发现 */
void  aw_provide(aw_ctx *ctx, const char *name, const aj_value *meta);
char *aw_consume(aw_ctx *ctx, const char *name);   /* 返回 malloc 的插件 ID，调用方 free */

/* ② 服务调用（返回的 aj_value 由调用方 aj_free） */
aj_value *aw_invoke(aw_ctx *ctx, const char *service, const char *method,
                    const aj_value *args);

/* ③ 事件总线 */
void aw_emit(aw_ctx *ctx, const char *event, const aj_value *payload);
void aw_on(aw_ctx *ctx, const char *event, aw_event_fn handler);   /* "*" 订阅全部 */

/* ④ 配置管理 */
aj_value *aw_get_config(aw_ctx *ctx, const char *key);             /* 无此键返回 NULL */
void      aw_set_config(aw_ctx *ctx, const char *key, const aj_value *value);

/* ⑤ 日志（写 stderr，核心转发到 logs/<pluginId>.log） */
void aw_log(aw_ctx *ctx, const char *level, const char *message);
void aw_logf(const char *level, const char *format, ...);

/* ⑥ 沙箱（接口预留）与能力代理（规范 3.5） */
aj_value *aw_sandbox_run(aw_ctx *ctx, const char *code, const aj_value *options);
aj_value *aw_capability(aw_ctx *ctx, const char *method, const aj_value *params);

/* 出错时在 handler 里调用，然后 return NULL */
void aw_fail(aw_ctx *ctx, int code, const char *message);

/* core.init 里核心告知的信息 */
const char *aw_plugin_id(aw_ctx *ctx);
const char *aw_api_version(aw_ctx *ctx);
const char *aw_core_version(aw_ctx *ctx);

#ifdef __cplusplus
}
#endif

#endif /* AIWORK_PLUGIN_H */
