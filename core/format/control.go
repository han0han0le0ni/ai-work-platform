package format

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// UIOpener 是可选扩展：核心若支持「按需把界面插件重新拉起来」，就实现它。
//
// 典型场景：用户关掉 GUI 窗口后核心仍在后台运行（控制端口没释放），
// 再点一次启动器时，启动器通过 /ui/open 让核心重新加载界面插件。
type UIOpener interface {
	OpenUI() (loaded bool, err error)
}

// ControlHandler 返回挂载在 <host>:<port> 上的控制通道。
//
// 格式化必须物理接触或预置 Token：
//
//	POST /format  Header: X-Format-Token: <core.json 中的 controlToken>
//
// 打开界面同样要求 Token：
//
//	POST /ui/open  Header: X-Format-Token: <core.json 中的 controlToken>
//
// 另提供 GET /health 与 GET /info 便于探测。
func ControlHandler(h Host, token string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/format", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		given := r.Header.Get("X-Format-Token")
		if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "forbidden: token 不匹配", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("formatting\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(120 * time.Millisecond)
			_ = Reset(h)
		}()
	})

	mux.HandleFunc("/ui/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		given := r.Header.Get("X-Format-Token")
		if token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "forbidden: token 不匹配", http.StatusForbidden)
			return
		}
		opener, ok := h.(UIOpener)
		if !ok {
			http.Error(w, "这个核心不支持按需打开界面", http.StatusNotImplemented)
			return
		}
		loaded, err := opener.OpenUI()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"ok":false,"error":%q}`, err.Error())
			return
		}
		fmt.Fprintf(w, `{"ok":true,"loaded":%t}`, loaded)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	return mux
}

// StartControlServer 监听本地端口。端口不可用时返回错误，但不阻塞核心启动。
//
// 重启场景下旧监听可能仍处于 TIME_WAIT，因此短暂重试再放弃。
func StartControlServer(h Host, host string, port int, token string) (io.Closer, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, port)

	var ln net.Listener
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           ControlHandler(h, token),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
