// readiness.go 就绪检查处理器 用于处理函数的就绪状态检查
package pkg

import (
	"log"
	"net/http"
	"net/url"
	"sync/atomic"

	limiter "github.com/openfaas/faas-middleware/concurrency-limiter"
)

// readiness 结构体用于处理函数的就绪状态检查
type readiness struct {
	// functionHandler 是函数调用的 HTTP Handler
	// 通过它可以在所有调用模式下实现自定义的就绪检查
	// 例如，在 fork 模式下，handler 实现（可能是 bash 脚本）可以检查环境中的路径，
	// 并根据结果响应，未就绪时退出码非零
	functionHandler http.Handler

	// endpoint 表示函数内部的就绪检查路径
	endpoint string

	// lockCheck 是一个函数，用于判断是否允许接收请求
	lockCheck func() bool

	// limiter 用于并发限制
	limiter limiter.Limiter
}

// LimitMet 判断是否达到并发限制
// 如果未使用 limiter，则返回 false
func (r *readiness) LimitMet() bool {
	if r.limiter == nil {
		return false
	}
	return r.limiter.Met()
}

// ServeHTTP 实现了 http.Handler 接口，用于处理就绪检查请求
func (r *readiness) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet: // 只处理 GET 请求
		status := http.StatusOK // 默认状态为 200 OK

		switch {
		// 如果系统不接受请求或 lockCheck 返回 false，则返回 503
		case atomic.LoadInt32(&acceptingConnections) == 0, !r.lockCheck():
			status = http.StatusServiceUnavailable

		// 如果并发限制已达到，则返回 429
		case r.LimitMet():
			status = http.StatusTooManyRequests

		// 如果配置了 endpoint，则向函数内部发起就绪检查
		case r.endpoint != "":
			upstream := url.URL{
				Scheme: req.URL.Scheme, // 保留原请求的协议
				Host:   req.URL.Host,   // 保留原请求的主机
				Path:   r.endpoint,     // 使用就绪检查路径
			}

			// 创建新的 HTTP GET 请求
			readyReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, upstream.String(), nil)
			if err != nil {
				log.Printf("Error creating readiness request to: %s : %s", upstream.String(), err)
				status = http.StatusInternalServerError
				break
			}

			// 设置原始 RequestURI，保证函数调用者能够看到完整路径
			// 否则默认会路由到 `/`
			readyReq.RequestURI = r.endpoint
			readyReq.Header = req.Header.Clone() // 克隆原请求的 header

			// 直接调用 functionHandler 处理请求
			// 对于 bash 或其他 fork 模式的函数，这里可以 fork 一个进程执行请求
			r.functionHandler.ServeHTTP(w, readyReq)
			return
		}

		// 返回最终状态码
		w.WriteHeader(status)
	default:
		// 非 GET 请求返回 405 方法不允许
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
