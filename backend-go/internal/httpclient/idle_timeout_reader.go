package httpclient

import (
	"errors"
	"io"
	"log"
	"time"
)

// ErrStreamIdleTimeout 流式响应空闲超时错误。
// 当上游"TCP 通但流中长时间无数据"时由 IdleTimeoutReader 返回，用于检测流中挂起。
var ErrStreamIdleTimeout = errors.New("stream idle timeout: upstream sent no data for the idle timeout period")

// IdleTimeoutReader 包装 io.ReadCloser，在读操作时检测空闲超时。
//
// 背景：流式响应使用无整体超时的客户端（避免误杀思考模型的长静默期），
// 导致"上游挂起（TCP 通但永不发数据）"时 handler 无限等待。
// 本 reader 在每次 Read 时 select 空闲超时：底层 Read 阻塞期间若超过
// idleTimeout 无数据，则关闭底层连接解阻塞，返回 ErrStreamIdleTimeout。
//
// 设计要点：
//   - 每次成功读取后重置计时（每次 Read 独立计时），长流中持续有数据不受影响；
//   - 默认值应保守（如 5 分钟），避免思考模型 1-2 分钟无输出被误杀；
//   - 仅对流式响应启用，普通请求不走此包装；
//   - goroutine 每次 Read 最多一个，读取返回或超时关闭后即退出，无泄漏。
func NewIdleTimeoutReader(inner io.ReadCloser, idleTimeout time.Duration, apiType string) io.ReadCloser {
	if idleTimeout <= 0 {
		return inner // 未配置空闲超时，直接透传
	}
	return &idleTimeoutReader{
		inner:       inner,
		idleTimeout: idleTimeout,
		apiType:     apiType,
	}
}

type idleTimeoutReader struct {
	inner       io.ReadCloser
	idleTimeout time.Duration
	apiType     string
}

type readResult struct {
	n   int
	err error
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	// 启动一个 goroutine 执行阻塞读，主路径 select 空闲超时。
	done := make(chan readResult, 1)
	go func() {
		n, err := r.inner.Read(p)
		done <- readResult{n: n, err: err}
	}()

	select {
	case res := <-done:
		return res.n, res.err
	case <-time.After(r.idleTimeout):
		// 空闲超时：关闭底层连接以解阻塞 pending Read，返回超时错误。
		log.Printf("[%s-Stream-IdleTimeout] 警告: 流式响应空闲超时（%.0f 秒无数据），判定上游挂起，已中止流", r.apiType, r.idleTimeout.Seconds())
		_ = r.inner.Close()
		return 0, ErrStreamIdleTimeout
	}
}

func (r *idleTimeoutReader) Close() error {
	return r.inner.Close()
}
