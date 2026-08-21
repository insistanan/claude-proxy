package providers

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// streamPump 聚合各 provider 流式实现共有的脚手架：事件/错误双通道、
// 带断连中止的 send/fail、上游 SSE scanner 构造。四个协议的
// HandleStreamResponseCtx 共用此骨架，协议差异（事件合成、收尾动作）
// 留在各自的循环体内。
//
// 约定：pump goroutine 必须 defer close 双通道。消费方
// （handlers/streams.ProcessStreamEvents）对 errChan 关闭有专门分支；
// close 不清除已缓冲的错误，fail 先入队的错误仍会被消费。
type streamPump struct {
	ctx       context.Context
	eventChan chan string
	errChan   chan error
}

func newStreamPump(ctx context.Context) *streamPump {
	return &streamPump{
		ctx:       ctx,
		eventChan: make(chan string, 100),
		errChan:   make(chan error, 1),
	}
}

// send 向 eventChan 发送一个事件；客户端断连（ctx 取消）时返回 false，调用方应立即退出。
func (p *streamPump) send(event string) bool {
	select {
	case p.eventChan <- event:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// fail 向 errChan 发送错误；客户端断连时不阻塞。
func (p *streamPump) fail(err error) {
	select {
	case p.errChan <- err:
	case <-p.ctx.Done():
	}
}

// newScanner 构造上游 SSE scanner：初始 64KB、上限 1MB 缓冲，
// 处理大 JSON chunk，避免默认 64KB 限制截断。
func (p *streamPump) newScanner(body io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(body)
	const maxScannerBufferSize = 1024 * 1024 // 1MB
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBufferSize)
	return scanner
}

// isDisconnectLikeError 判断 scanner 错误是否为连接断开类（客户端主动
// 断开或上游中断）。各 provider 对这类错误的收尾动作不同（吞掉 /
// 补 message_stop / 按 tool_use 状态区分），但谓词语义一致。
func isDisconnectLikeError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF")
}
