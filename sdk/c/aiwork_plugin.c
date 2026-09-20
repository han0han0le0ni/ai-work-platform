/* AI 工作平台插件 SDK（C99）实现：极简 JSON DOM + 插件协议。
 *
 * 与 aiwork_plugin.h 一起编进你的插件即可，无第三方依赖。
 */

#include "aiwork_plugin.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#endif

/* ================================================================== */
/* 基础工具                                                            */
/* ================================================================== */

static void *xmalloc(size_t n)
{
    void *p = malloc(n ? n : 1);
    if (!p) {
        fputs("[error] 内存不足\n", stderr);
        exit(70);
    }
    return p;
}

static void *xrealloc(void *p, size_t n)
{
    void *q = realloc(p, n ? n : 1);
    if (!q) {
        fputs("[error] 内存不足\n", stderr);
        exit(70);
    }
    return q;
}

static char *xstrndup(const char *s, size_t n)
{
    char *p = (char *)xmalloc(n + 1);
    if (n && s) {
        memcpy(p, s, n);
    }
    p[n] = '\0';
    return p;
}

static char *xstrdup(const char *s)
{
    return s ? xstrndup(s, strlen(s)) : xstrndup("", 0);
}

/* 可变长字符串缓冲 */
typedef struct {
    char *ptr;
    size_t len;
    size_t cap;
} sbuf;

static void sbuf_init(sbuf *b)
{
    b->cap = 128;
    b->len = 0;
    b->ptr = (char *)xmalloc(b->cap);
    b->ptr[0] = '\0';
}

static void sbuf_reserve(sbuf *b, size_t extra)
{
    if (b->len + extra + 1 <= b->cap) {
        return;
    }
    while (b->len + extra + 1 > b->cap) {
        b->cap *= 2;
    }
    b->ptr = (char *)xrealloc(b->ptr, b->cap);
}

static void sbuf_putc(sbuf *b, char c)
{
    sbuf_reserve(b, 1);
    b->ptr[b->len++] = c;
    b->ptr[b->len] = '\0';
}

static void sbuf_putn(sbuf *b, const char *s, size_t n)
{
    if (!s || n == 0) {
        return;
    }
    sbuf_reserve(b, n);
    memcpy(b->ptr + b->len, s, n);
    b->len += n;
    b->ptr[b->len] = '\0';
}

static void sbuf_puts(sbuf *b, const char *s)
{
    if (s) {
        sbuf_putn(b, s, strlen(s));
    }
}

static char *sbuf_take(sbuf *b)
{
    char *out = b->ptr;
    b->ptr = NULL;
    b->len = b->cap = 0;
    return out;
}

/* 把码点写成 UTF-8 */
static void sbuf_putcp(sbuf *b, unsigned int cp)
{
    if (cp < 0x80) {
        sbuf_putc(b, (char)cp);
    } else if (cp < 0x800) {
        sbuf_putc(b, (char)(0xC0 | (cp >> 6)));
        sbuf_putc(b, (char)(0x80 | (cp & 0x3F)));
    } else if (cp < 0x10000) {
        sbuf_putc(b, (char)(0xE0 | (cp >> 12)));
        sbuf_putc(b, (char)(0x80 | ((cp >> 6) & 0x3F)));
        sbuf_putc(b, (char)(0x80 | (cp & 0x3F)));
    } else {
        sbuf_putc(b, (char)(0xF0 | (cp >> 18)));
        sbuf_putc(b, (char)(0x80 | ((cp >> 12) & 0x3F)));
        sbuf_putc(b, (char)(0x80 | ((cp >> 6) & 0x3F)));
        sbuf_putc(b, (char)(0x80 | (cp & 0x3F)));
    }
}

/* ================================================================== */
/* JSON DOM                                                            */
/* ================================================================== */

struct aj_value {
    aj_type type;
    union {
        int boolean;
        double num;
        char *str;
        struct {
            aj_value **items;
            size_t count;
            size_t cap;
        } arr;
        struct {
            char **keys;
            aj_value **vals;
            size_t count;
            size_t cap;
        } obj;
    } u;
};

static aj_value *aj_new(aj_type type)
{
    aj_value *v = (aj_value *)xmalloc(sizeof(aj_value));
    memset(v, 0, sizeof(aj_value));
    v->type = type;
    return v;
}

aj_value *aj_null(void) { return aj_new(AJ_NULL); }

aj_value *aj_bool(int value)
{
    aj_value *v = aj_new(AJ_BOOL);
    v->u.boolean = value ? 1 : 0;
    return v;
}

aj_value *aj_num(double value)
{
    aj_value *v = aj_new(AJ_NUM);
    v->u.num = value;
    return v;
}

aj_value *aj_str(const char *value)
{
    aj_value *v = aj_new(AJ_STR);
    v->u.str = xstrdup(value);
    return v;
}

aj_value *aj_arr(void) { return aj_new(AJ_ARR); }
aj_value *aj_obj(void) { return aj_new(AJ_OBJ); }

void aj_arr_push(aj_value *array, aj_value *item)
{
    if (!array || array->type != AJ_ARR) {
        aj_free(item);
        return;
    }
    if (array->u.arr.count == array->u.arr.cap) {
        array->u.arr.cap = array->u.arr.cap ? array->u.arr.cap * 2 : 8;
        array->u.arr.items = (aj_value **)xrealloc(
            array->u.arr.items, array->u.arr.cap * sizeof(aj_value *));
    }
    array->u.arr.items[array->u.arr.count++] = item;
}

void aj_obj_set(aj_value *object, const char *key, aj_value *value)
{
    size_t i;
    if (!object || object->type != AJ_OBJ || !key) {
        aj_free(value);
        return;
    }
    /* 同键覆盖，保证一个键只有一个值 */
    for (i = 0; i < object->u.obj.count; i++) {
        if (strcmp(object->u.obj.keys[i], key) == 0) {
            aj_free(object->u.obj.vals[i]);
            object->u.obj.vals[i] = value;
            return;
        }
    }
    if (object->u.obj.count == object->u.obj.cap) {
        object->u.obj.cap = object->u.obj.cap ? object->u.obj.cap * 2 : 8;
        object->u.obj.keys = (char **)xrealloc(object->u.obj.keys,
                                               object->u.obj.cap * sizeof(char *));
        object->u.obj.vals = (aj_value **)xrealloc(
            object->u.obj.vals, object->u.obj.cap * sizeof(aj_value *));
    }
    object->u.obj.keys[object->u.obj.count] = xstrdup(key);
    object->u.obj.vals[object->u.obj.count] = value;
    object->u.obj.count++;
}

void aj_free(aj_value *value)
{
    size_t i;
    if (!value) {
        return;
    }
    switch (value->type) {
    case AJ_STR:
        free(value->u.str);
        break;
    case AJ_ARR:
        for (i = 0; i < value->u.arr.count; i++) {
            aj_free(value->u.arr.items[i]);
        }
        free(value->u.arr.items);
        break;
    case AJ_OBJ:
        for (i = 0; i < value->u.obj.count; i++) {
            free(value->u.obj.keys[i]);
            aj_free(value->u.obj.vals[i]);
        }
        free(value->u.obj.keys);
        free(value->u.obj.vals);
        break;
    default:
        break;
    }
    free(value);
}

aj_value *aj_clone(const aj_value *value)
{
    size_t i;
    if (!value) {
        return aj_null();
    }
    switch (value->type) {
    case AJ_NULL: return aj_null();
    case AJ_BOOL: return aj_bool(value->u.boolean);
    case AJ_NUM:  return aj_num(value->u.num);
    case AJ_STR:  return aj_str(value->u.str);
    case AJ_ARR: {
        aj_value *out = aj_arr();
        for (i = 0; i < value->u.arr.count; i++) {
            aj_arr_push(out, aj_clone(value->u.arr.items[i]));
        }
        return out;
    }
    case AJ_OBJ: {
        aj_value *out = aj_obj();
        for (i = 0; i < value->u.obj.count; i++) {
            aj_obj_set(out, value->u.obj.keys[i], aj_clone(value->u.obj.vals[i]));
        }
        return out;
    }
    default:
        return aj_null();
    }
}

aj_type aj_type_of(const aj_value *value) { return value ? value->type : AJ_NULL; }

const aj_value *aj_get(const aj_value *object, const char *key)
{
    size_t i;
    if (!object || object->type != AJ_OBJ || !key) {
        return NULL;
    }
    for (i = 0; i < object->u.obj.count; i++) {
        if (strcmp(object->u.obj.keys[i], key) == 0) {
            return object->u.obj.vals[i];
        }
    }
    return NULL;
}

const char *aj_get_str(const aj_value *object, const char *key, const char *fallback)
{
    return aj_as_str(aj_get(object, key), fallback);
}

double aj_get_num(const aj_value *object, const char *key, double fallback)
{
    return aj_as_num(aj_get(object, key), fallback);
}

int aj_get_bool(const aj_value *object, const char *key, int fallback)
{
    return aj_as_bool(aj_get(object, key), fallback);
}

const char *aj_as_str(const aj_value *value, const char *fallback)
{
    if (value && value->type == AJ_STR) {
        return value->u.str;
    }
    return fallback;
}

double aj_as_num(const aj_value *value, double fallback)
{
    return (value && value->type == AJ_NUM) ? value->u.num : fallback;
}

int aj_as_bool(const aj_value *value, int fallback)
{
    return (value && value->type == AJ_BOOL) ? value->u.boolean : fallback;
}

size_t aj_count(const aj_value *value)
{
    if (!value) {
        return 0;
    }
    if (value->type == AJ_ARR) {
        return value->u.arr.count;
    }
    if (value->type == AJ_OBJ) {
        return value->u.obj.count;
    }
    return 0;
}

const aj_value *aj_at(const aj_value *array, size_t index)
{
    if (!array || array->type != AJ_ARR || index >= array->u.arr.count) {
        return NULL;
    }
    return array->u.arr.items[index];
}

const char *aj_key_at(const aj_value *object, size_t index)
{
    if (!object || object->type != AJ_OBJ || index >= object->u.obj.count) {
        return NULL;
    }
    return object->u.obj.keys[index];
}

/* ---------------------------- 解析 ---------------------------- */

typedef struct {
    const char *src;
    size_t len;
    size_t pos;
    int failed;
} jparser;

static void jp_ws(jparser *p)
{
    while (p->pos < p->len) {
        char c = p->src[p->pos];
        if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
            p->pos++;
        } else {
            break;
        }
    }
}

static aj_value *jp_value(jparser *p);

static char *jp_string(jparser *p)
{
    sbuf b;
    sbuf_init(&b);
    p->pos++; /* 开引号 */
    while (p->pos < p->len) {
        char c = p->src[p->pos++];
        if (c == '"') {
            return sbuf_take(&b);
        }
        if (c != '\\') {
            sbuf_putc(&b, c);
            continue;
        }
        if (p->pos >= p->len) {
            break;
        }
        c = p->src[p->pos++];
        switch (c) {
        case '"':  sbuf_putc(&b, '"');  break;
        case '\\': sbuf_putc(&b, '\\'); break;
        case '/':  sbuf_putc(&b, '/');  break;
        case 'b':  sbuf_putc(&b, '\b'); break;
        case 'f':  sbuf_putc(&b, '\f'); break;
        case 'n':  sbuf_putc(&b, '\n'); break;
        case 'r':  sbuf_putc(&b, '\r'); break;
        case 't':  sbuf_putc(&b, '\t'); break;
        case 'u': {
            unsigned int cp = 0;
            int i;
            if (p->pos + 4 > p->len) {
                p->failed = 1;
                break;
            }
            for (i = 0; i < 4; i++) {
                char h = p->src[p->pos++];
                cp <<= 4;
                if (h >= '0' && h <= '9') {
                    cp |= (unsigned int)(h - '0');
                } else if (h >= 'a' && h <= 'f') {
                    cp |= (unsigned int)(h - 'a' + 10);
                } else if (h >= 'A' && h <= 'F') {
                    cp |= (unsigned int)(h - 'A' + 10);
                } else {
                    p->failed = 1;
                }
            }
            /* 代理对：把 \uD83C\uDF00 之类合成一个码点 */
            if (cp >= 0xD800 && cp <= 0xDBFF && p->pos + 6 <= p->len &&
                p->src[p->pos] == '\\' && p->src[p->pos + 1] == 'u') {
                unsigned int low = 0;
                size_t save = p->pos;
                p->pos += 2;
                for (i = 0; i < 4; i++) {
                    char h = p->src[p->pos++];
                    low <<= 4;
                    if (h >= '0' && h <= '9') {
                        low |= (unsigned int)(h - '0');
                    } else if (h >= 'a' && h <= 'f') {
                        low |= (unsigned int)(h - 'a' + 10);
                    } else if (h >= 'A' && h <= 'F') {
                        low |= (unsigned int)(h - 'A' + 10);
                    } else {
                        p->pos = save;
                        low = 0;
                        break;
                    }
                }
                if (low >= 0xDC00 && low <= 0xDFFF) {
                    cp = 0x10000 + ((cp - 0xD800) << 10) + (low - 0xDC00);
                } else {
                    p->pos = save;
                }
            }
            sbuf_putcp(&b, cp);
            break;
        }
        default:
            p->failed = 1;
            break;
        }
    }
    p->failed = 1;
    free(b.ptr);
    return NULL;
}

static aj_value *jp_number(jparser *p)
{
    size_t start = p->pos;
    int is_float = 0;
    char *raw;
    aj_value *v;

    if (p->pos < p->len && (p->src[p->pos] == '-' || p->src[p->pos] == '+')) {
        p->pos++;
    }
    while (p->pos < p->len) {
        char c = p->src[p->pos];
        if (c >= '0' && c <= '9') {
            p->pos++;
        } else if (c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
            if (c == '.' || c == 'e' || c == 'E') {
                is_float = 1;
            }
            p->pos++;
        } else {
            break;
        }
    }
    if (p->pos == start) {
        p->failed = 1;
        return NULL;
    }
    raw = xstrndup(p->src + start, p->pos - start);
    v = aj_num(strtod(raw, NULL));
    free(raw);
    (void)is_float;
    return v;
}

static aj_value *jp_array(jparser *p)
{
    aj_value *arr = aj_arr();
    p->pos++; /* [ */
    jp_ws(p);
    if (p->pos < p->len && p->src[p->pos] == ']') {
        p->pos++;
        return arr;
    }
    for (;;) {
        aj_value *item = jp_value(p);
        if (p->failed) {
            aj_free(arr);
            return NULL;
        }
        aj_arr_push(arr, item);
        jp_ws(p);
        if (p->pos >= p->len) {
            break;
        }
        if (p->src[p->pos] == ',') {
            p->pos++;
            continue;
        }
        if (p->src[p->pos] == ']') {
            p->pos++;
            return arr;
        }
        break;
    }
    p->failed = 1;
    aj_free(arr);
    return NULL;
}

static aj_value *jp_object(jparser *p)
{
    aj_value *obj = aj_obj();
    p->pos++; /* { */
    jp_ws(p);
    if (p->pos < p->len && p->src[p->pos] == '}') {
        p->pos++;
        return obj;
    }
    for (;;) {
        char *key;
        aj_value *val;
        jp_ws(p);
        if (p->pos >= p->len || p->src[p->pos] != '"') {
            break;
        }
        key = jp_string(p);
        if (p->failed) {
            break;
        }
        jp_ws(p);
        if (p->pos >= p->len || p->src[p->pos] != ':') {
            free(key);
            break;
        }
        p->pos++;
        val = jp_value(p);
        if (p->failed) {
            free(key);
            break;
        }
        aj_obj_set(obj, key, val);
        free(key);
        jp_ws(p);
        if (p->pos >= p->len) {
            break;
        }
        if (p->src[p->pos] == ',') {
            p->pos++;
            continue;
        }
        if (p->src[p->pos] == '}') {
            p->pos++;
            return obj;
        }
        break;
    }
    p->failed = 1;
    aj_free(obj);
    return NULL;
}

static aj_value *jp_value(jparser *p)
{
    char c;
    jp_ws(p);
    if (p->pos >= p->len) {
        p->failed = 1;
        return NULL;
    }
    c = p->src[p->pos];
    switch (c) {
    case '{': return jp_object(p);
    case '[': return jp_array(p);
    case '"': {
        char *s = jp_string(p);
        aj_value *v;
        if (p->failed) {
            free(s);
            return NULL;
        }
        v = aj_str(s);
        free(s);
        return v;
    }
    case 't':
        if (p->pos + 4 <= p->len && strncmp(p->src + p->pos, "true", 4) == 0) {
            p->pos += 4;
            return aj_bool(1);
        }
        break;
    case 'f':
        if (p->pos + 5 <= p->len && strncmp(p->src + p->pos, "false", 5) == 0) {
            p->pos += 5;
            return aj_bool(0);
        }
        break;
    case 'n':
        if (p->pos + 4 <= p->len && strncmp(p->src + p->pos, "null", 4) == 0) {
            p->pos += 4;
            return aj_null();
        }
        break;
    default:
        return jp_number(p);
    }
    p->failed = 1;
    return NULL;
}

aj_value *aj_parsen(const char *text, size_t len)
{
    jparser p;
    aj_value *value;
    if (!text) {
        return NULL;
    }
    p.src = text;
    p.len = len;
    p.pos = 0;
    p.failed = 0;
    value = jp_value(&p);
    if (p.failed) {
        aj_free(value);
        return NULL;
    }
    jp_ws(&p);
    if (p.pos != p.len) { /* 尾部有多余内容 */
        aj_free(value);
        return NULL;
    }
    return value;
}

aj_value *aj_parse(const char *text)
{
    return text ? aj_parsen(text, strlen(text)) : NULL;
}

/* ---------------------------- 序列化 ---------------------------- */

static void jw_value(sbuf *b, const aj_value *v)
{
    size_t i;
    char num[64];
    if (!v) {
        sbuf_puts(b, "null");
        return;
    }
    switch (v->type) {
    case AJ_NULL:
        sbuf_puts(b, "null");
        break;
    case AJ_BOOL:
        sbuf_puts(b, v->u.boolean ? "true" : "false");
        break;
    case AJ_NUM: {
        double d = v->u.num;
        if (d == (double)(long long)d && d > -1e15 && d < 1e15) {
            sprintf(num, "%lld", (long long)d);
        } else {
            sprintf(num, "%.17g", d);
        }
        sbuf_puts(b, num);
        break;
    }
    case AJ_STR: {
        const unsigned char *s = (const unsigned char *)(v->u.str ? v->u.str : "");
        sbuf_putc(b, '"');
        for (; *s; s++) {
            switch (*s) {
            case '"':  sbuf_puts(b, "\\\""); break;
            case '\\': sbuf_puts(b, "\\\\"); break;
            case '\n': sbuf_puts(b, "\\n"); break;
            case '\r': sbuf_puts(b, "\\r"); break;
            case '\t': sbuf_puts(b, "\\t"); break;
            case '\b': sbuf_puts(b, "\\b"); break;
            case '\f': sbuf_puts(b, "\\f"); break;
            default:
                if (*s < 0x20) {
                    sprintf(num, "\\u%04x", (unsigned int)*s);
                    sbuf_puts(b, num);
                } else {
                    sbuf_putc(b, (char)*s);
                }
            }
        }
        sbuf_putc(b, '"');
        break;
    }
    case AJ_ARR:
        sbuf_putc(b, '[');
        for (i = 0; i < v->u.arr.count; i++) {
            if (i) {
                sbuf_putc(b, ',');
            }
            jw_value(b, v->u.arr.items[i]);
        }
        sbuf_putc(b, ']');
        break;
    case AJ_OBJ:
        sbuf_putc(b, '{');
        for (i = 0; i < v->u.obj.count; i++) {
            aj_value key;
            memset(&key, 0, sizeof(key));
            key.type = AJ_STR;
            key.u.str = v->u.obj.keys[i];
            if (i) {
                sbuf_putc(b, ',');
            }
            jw_value(b, &key);
            sbuf_putc(b, ':');
            jw_value(b, v->u.obj.vals[i]);
        }
        sbuf_putc(b, '}');
        break;
    default:
        sbuf_puts(b, "null");
    }
}

char *aj_write(const aj_value *value)
{
    sbuf b;
    sbuf_init(&b);
    jw_value(&b, value);
    return sbuf_take(&b);
}

/* ================================================================== */
/* 插件协议                                                            */
/* ================================================================== */

typedef struct aw_sub {
    char *event;
    aw_event_fn fn;
    struct aw_sub *next;
} aw_sub;

typedef struct aw_stash {
    long id;
    aj_value *message;
    struct aw_stash *next;
} aw_stash;

struct aw_ctx {
    const aw_plugin *plugin;
    char *plugin_id;
    char *api_version;
    char *core_version;
    char *config_namespace;
    aw_sub *subs;
    aw_stash *stash;
    long next_id;
    int has_error;
    int err_code;
    char *err_message;
};

static const aw_plugin empty_plugin = {NULL, NULL, NULL};

static aw_ctx *g_ctx = NULL;

void aw_logf(const char *level, const char *format, ...)
{
    va_list args;
    fprintf(stderr, "[%s] ", level);
    va_start(args, format);
    vfprintf(stderr, format, args);
    va_end(args);
    fputc('\n', stderr);
    fflush(stderr);
}

void aw_log(aw_ctx *ctx, const char *level, const char *message)
{
    (void)ctx;
    aw_logf(level ? level : "info", "%s", message ? message : "");
}

const char *aw_plugin_id(aw_ctx *ctx) { return ctx && ctx->plugin_id ? ctx->plugin_id : ""; }
const char *aw_api_version(aw_ctx *ctx) { return ctx && ctx->api_version ? ctx->api_version : ""; }
const char *aw_core_version(aw_ctx *ctx) { return ctx && ctx->core_version ? ctx->core_version : ""; }

void aw_fail(aw_ctx *ctx, int code, const char *message)
{
    if (!ctx) {
        return;
    }
    free(ctx->err_message);
    ctx->err_message = xstrdup(message ? message : "");
    ctx->err_code = code;
    ctx->has_error = 1;
}

/* ---------------------------- 读写 ---------------------------- */

static void write_line(const char *text)
{
    fputs(text, stdout);
    fputc('\n', stdout);
    fflush(stdout);
}

static void write_message(const aj_value *message)
{
    char *text = aj_write(message);
    write_line(text);
    free(text);
}

/* 读一行（不含换行）。返回 malloc 的内存；EOF 返回 NULL。 */
static char *read_line(void)
{
    sbuf b;
    int got = 0;
    sbuf_init(&b);
    for (;;) {
        int c = fgetc(stdin);
        if (c == EOF) {
            if (!got) {
                free(b.ptr);
                return NULL;
            }
            break;
        }
        got = 1;
        if (c == '\n') {
            break;
        }
        sbuf_putc(&b, (char)c);
    }
    while (b.len > 0 && (b.ptr[b.len - 1] == '\r' || b.ptr[b.len - 1] == '\n')) {
        b.ptr[--b.len] = '\0';
    }
    if (b.len >= 3 && (unsigned char)b.ptr[0] == 0xEF && (unsigned char)b.ptr[1] == 0xBB &&
        (unsigned char)b.ptr[2] == 0xBF) {
        memmove(b.ptr, b.ptr + 3, b.len - 3 + 1);
        b.len -= 3;
    }
    return sbuf_take(&b);
}

/* 注意：这里复制 result，所有权仍归调用方，避免双重释放 */
static void respond(aw_ctx *ctx, const aj_value *id, const aj_value *result)
{
    aj_value *msg = aj_obj();
    (void)ctx;
    aj_obj_set(msg, "jsonrpc", aj_str("2.0"));
    aj_obj_set(msg, "id", aj_clone(id));
    aj_obj_set(msg, "result", result ? aj_clone(result) : aj_null());
    write_message(msg);
    aj_free(msg);
}

static void respond_error(aw_ctx *ctx, const aj_value *id, int code, const char *message)
{
    aj_value *msg = aj_obj();
    aj_value *err = aj_obj();
    (void)ctx;
    aj_obj_set(err, "code", aj_num((double)code));
    aj_obj_set(err, "message", aj_str(message ? message : ""));
    aj_obj_set(msg, "jsonrpc", aj_str("2.0"));
    aj_obj_set(msg, "id", aj_clone(id));
    aj_obj_set(msg, "error", err);
    write_message(msg);
    aj_free(msg);
}

/* ---------------------------- 出站调用 ---------------------------- */

static void stash_put(aw_ctx *ctx, long id, aj_value *message)
{
    aw_stash *node = (aw_stash *)xmalloc(sizeof(aw_stash));
    node->id = id;
    node->message = message;
    node->next = ctx->stash;
    ctx->stash = node;
}

static aj_value *stash_take(aw_ctx *ctx, long id)
{
    aw_stash **pp = &ctx->stash;
    while (*pp) {
        if ((*pp)->id == id) {
            aw_stash *hit = *pp;
            aj_value *message = hit->message;
            *pp = hit->next;
            free(hit);
            return message;
        }
        pp = &(*pp)->next;
    }
    return NULL;
}

static void dispatch(aw_ctx *ctx, aj_value *message);

/* 等到 id 对应的响应。期间到达的消息就地处理（可重入）。 */
static aj_value *wait_response(aw_ctx *ctx, long id)
{
    aj_value *early = stash_take(ctx, id);
    if (early) {
        return early;
    }
    for (;;) {
        char *line = read_line();
        aj_value *message;
        if (!line) {
            return NULL; /* EOF：核心没了 */
        }
        message = aj_parse(line);
        free(line);
        if (!message) {
            aw_logf("error", "无法解析来自核心的消息");
            continue;
        }
        if (aj_get(message, "method") != NULL) {
            dispatch(ctx, message);
            aj_free(message);
            continue;
        }
        {
            long rid = (long)aj_get_num(message, "id", -1);
            if (rid == id) {
                return message;
            }
            stash_put(ctx, rid, message); /* 不是给我们的，先存着 */
        }
    }
}

static aj_value *aw_call(aw_ctx *ctx, const char *method, const aj_value *params)
{
    aj_value *req = aj_obj();
    aj_value *response;
    long id = ++ctx->next_id;

    aj_obj_set(req, "jsonrpc", aj_str("2.0"));
    aj_obj_set(req, "id", aj_num((double)id));
    aj_obj_set(req, "method", aj_str(method));
    aj_obj_set(req, "params", params ? aj_clone(params) : aj_null());
    write_message(req);
    aj_free(req);

    response = wait_response(ctx, id);
    if (!response) {
        aw_fail(ctx, AW_PROVIDER_DEAD, "核心已断开");
        return NULL;
    }
    if (aj_get(response, "error") != NULL) {
        const aj_value *err = aj_get(response, "error");
        aw_fail(ctx, (int)aj_get_num(err, "code", AW_INTERNAL_ERROR),
                aj_get_str(err, "message", "未知错误"));
        aj_free(response);
        return NULL;
    }
    {
        aj_value *result = aj_clone(aj_get(response, "result"));
        aj_free(response);
        return result;
    }
}

static void notify(aw_ctx *ctx, const char *method, const aj_value *params)
{
    aj_value *msg = aj_obj();
    (void)ctx;
    aj_obj_set(msg, "jsonrpc", aj_str("2.0"));
    aj_obj_set(msg, "method", aj_str(method));
    aj_obj_set(msg, "params", params ? aj_clone(params) : aj_null());
    write_message(msg);
    aj_free(msg);
}

/* ---------------------------- 上下文 API ---------------------------- */

void aw_provide(aw_ctx *ctx, const char *name, const aj_value *meta)
{
    aj_value *params = aj_obj();
    aj_value *result;
    aj_obj_set(params, "name", aj_str(name));
    aj_obj_set(params, "meta", meta ? aj_clone(meta) : aj_null());
    result = aw_call(ctx, "core.provide", params);
    aj_free(params);
    aj_free(result);
}

char *aw_consume(aw_ctx *ctx, const char *name)
{
    aj_value *params = aj_obj();
    aj_value *result;
    char *plugin_id = NULL;
    aj_obj_set(params, "name", aj_str(name));
    result = aw_call(ctx, "core.consume", params);
    aj_free(params);
    if (result) {
        const char *id = aj_get_str(result, "pluginId", NULL);
        if (id) {
            plugin_id = xstrdup(id);
        }
        aj_free(result);
    }
    return plugin_id;
}

aj_value *aw_invoke(aw_ctx *ctx, const char *service, const char *method,
                    const aj_value *args)
{
    aj_value *params = aj_obj();
    aj_value *result;
    aj_obj_set(params, "service", aj_str(service));
    aj_obj_set(params, "method", aj_str(method));
    aj_obj_set(params, "args", args ? aj_clone(args) : aj_obj());
    result = aw_call(ctx, "core.invoke", params);
    aj_free(params);
    return result;
}

void aw_emit(aw_ctx *ctx, const char *event, const aj_value *payload)
{
    aj_value *params = aj_obj();
    aj_obj_set(params, "event", aj_str(event));
    aj_obj_set(params, "payload", payload ? aj_clone(payload) : aj_null());
    notify(ctx, "core.emit", params);
    aj_free(params);
}

void aw_on(aw_ctx *ctx, const char *event, aw_event_fn handler)
{
    aw_sub *node;
    if (!ctx || !event || !handler) {
        return;
    }
    node = (aw_sub *)xmalloc(sizeof(aw_sub));
    node->event = xstrdup(event);
    node->fn = handler;
    node->next = ctx->subs;
    ctx->subs = node;
}

aj_value *aw_get_config(aw_ctx *ctx, const char *key)
{
    aj_value *params = aj_obj();
    aj_value *result;
    aj_value *value = NULL;
    aj_obj_set(params, "key", aj_str(key));
    result = aw_call(ctx, "core.getConfig", params);
    aj_free(params);
    if (result) {
        if (aj_get_bool(result, "found", 0)) {
            value = aj_clone(aj_get(result, "value"));
        }
        aj_free(result);
    }
    return value;
}

void aw_set_config(aw_ctx *ctx, const char *key, const aj_value *value)
{
    aj_value *params = aj_obj();
    aj_value *result;
    aj_obj_set(params, "key", aj_str(key));
    aj_obj_set(params, "value", value ? aj_clone(value) : aj_null());
    result = aw_call(ctx, "core.setConfig", params);
    aj_free(params);
    aj_free(result);
}

aj_value *aw_sandbox_run(aw_ctx *ctx, const char *code, const aj_value *options)
{
    aj_value *params = aj_obj();
    aj_value *result;
    aj_obj_set(params, "code", aj_str(code));
    aj_obj_set(params, "options", options ? aj_clone(options) : aj_null());
    result = aw_call(ctx, "core.sandbox.run", params);
    aj_free(params);
    return result;
}

aj_value *aw_capability(aw_ctx *ctx, const char *method, const aj_value *params)
{
    return aw_call(ctx, method, params);
}

/* ---------------------------- 派发 ---------------------------- */

static void handle_init(aw_ctx *ctx, const aj_value *params)
{
    free(ctx->plugin_id);
    free(ctx->api_version);
    free(ctx->core_version);
    free(ctx->config_namespace);
    ctx->plugin_id = xstrdup(aj_get_str(params, "pluginId", ""));
    ctx->api_version = xstrdup(aj_get_str(params, "apiVersion", ""));
    ctx->core_version = xstrdup(aj_get_str(params, "coreVersion", ""));
    ctx->config_namespace = xstrdup(aj_get_str(params, "configNamespace", ctx->plugin_id));
    if (ctx->plugin->activate) {
        ctx->plugin->activate(ctx);
    }
}

static void handle_invoke(aw_ctx *ctx, const aj_value *id, const aj_value *params)
{
    const char *service = aj_get_str(params, "service", "");
    const char *method = aj_get_str(params, "method", "");
    const aj_value *args = aj_get(params, "args");
    aj_value *result;

    if (args && aj_type_of(args) != AJ_OBJ) {
        args = NULL;
    }
    ctx->has_error = 0;

    if (!ctx->plugin->handle) {
        respond_error(ctx, id, AW_METHOD_NOT_FOUND, "插件没有实现 handle");
        return;
    }
    result = ctx->plugin->handle(ctx, service, method, args);

    if (ctx->has_error) {
        respond_error(ctx, id, ctx->err_code, ctx->err_message);
        if (result) {
            aj_free(result);
        }
        ctx->has_error = 0;
        return;
    }
    respond(ctx, id, result);
    if (result) {
        aj_free(result);
    }
}

static void handle_event(aw_ctx *ctx, const aj_value *params)
{
    const char *event = aj_get_str(params, "event", "");
    const aj_value *payload = aj_get(params, "payload");
    aw_sub *node;
    for (node = ctx->subs; node; node = node->next) {
        if (strcmp(node->event, event) == 0 || strcmp(node->event, "*") == 0) {
            node->fn(ctx, payload);
        }
    }
}

static void handle_shutdown(aw_ctx *ctx)
{
    if (ctx->plugin->deactivate) {
        ctx->plugin->deactivate(ctx);
    }
    fflush(stdout);
    exit(0);
}

static void dispatch(aw_ctx *ctx, aj_value *message)
{
    const char *method = aj_get_str(message, "method", "");
    const aj_value *id = aj_get(message, "id");
    const aj_value *params = aj_get(message, "params");

    if (id == NULL) {
        /* 通知 */
        if (strcmp(method, "core.shutdown") == 0) {
            handle_shutdown(ctx);
        } else if (strcmp(method, "core.event") == 0) {
            handle_event(ctx, params);
        } else {
            aw_logf("warn", "收到未知通知 %s", method);
        }
        return;
    }
    if (strcmp(method, "core.init") == 0) {
        handle_init(ctx, params);
        {
            aj_value *result = aj_obj();
            aj_obj_set(result, "ready", aj_bool(1));
            respond(ctx, id, result);
            aj_free(result);
        }
        return;
    }
    if (strcmp(method, "core.invoke") == 0) {
        handle_invoke(ctx, id, params);
        return;
    }
    respond_error(ctx, id, AW_METHOD_NOT_FOUND, "插件不支持方法");
}

int aw_serve(const aw_plugin *plugin)
{
    aw_ctx ctx;

#ifdef _WIN32
    _setmode(_fileno(stdin), _O_BINARY);
    _setmode(_fileno(stdout), _O_BINARY);
#endif

    memset(&ctx, 0, sizeof(ctx));
    ctx.plugin = plugin ? plugin : &empty_plugin;
    ctx.plugin_id = xstrdup("");
    ctx.api_version = xstrdup("");
    ctx.core_version = xstrdup("");
    ctx.config_namespace = xstrdup("");
    g_ctx = &ctx;

    for (;;) {
        char *line = read_line();
        aj_value *message;
        if (!line) {
            break; /* stdin 关闭：核心退出 */
        }
        if (line[0] == '\0') {
            free(line);
            continue;
        }
        message = aj_parse(line);
        free(line);
        if (!message) {
            aw_logf("error", "无法解析来自核心的消息");
            continue;
        }
        dispatch(&ctx, message);
        aj_free(message);
    }
    return 0;
}
