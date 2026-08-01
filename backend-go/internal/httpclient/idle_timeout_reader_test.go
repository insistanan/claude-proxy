package httpclient

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type blockingReader struct{}

func (b *blockingReader) Read(p []byte) (int, error) {
	select {} // 永久阻塞，模拟上游挂起
}
func (b *blockingReader) Close() error { return nil }

type oneShotReader struct {
	read  bool
	inner io.Reader
}

func (o *oneShotReader) Read(p []byte) (int, error) {
	if o.read {
		return 0, io.EOF
	}
	o.read = true
	return o.inner.Read(p)
}
func (o *oneShotReader) Close() error { return nil }

// TestIdleTimeoutReader_TriggerOnHang 验证：上游挂起（Read 永久阻塞）时触发空闲超时。
func TestIdleTimeoutReader_TriggerOnHang(t *testing.T) {
	r := NewIdleTimeoutReader(&blockingReader{}, 100*time.Millisecond, "Test")
	buf := make([]byte, 1024)
	_, err := r.Read(buf)
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("期望 ErrStreamIdleTimeout，得到: %v", err)
	}
}

// TestIdleTimeoutReader_PassThrough 验证：正常数据立即返回，不受影响。
func TestIdleTimeoutReader_PassThrough(t *testing.T) {
	r := NewIdleTimeoutReader(&oneShotReader{inner: strings.NewReader("hello")}, 5*time.Second, "Test")
	buf := make([]byte, 1024)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("期望无错误，得到: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("期望 'hello'，得到: %q", string(buf[:n]))
	}
}

// TestIdleTimeoutReader_Disabled 验证：idleTimeout <= 0 时直接透传（不包装）。
func TestIdleTimeoutReader_Disabled(t *testing.T) {
	inner := &oneShotReader{inner: strings.NewReader("x")}
	r := NewIdleTimeoutReader(inner, 0, "Test")
	if r != io.ReadCloser(inner) {
		t.Fatal("idleTimeout<=0 时应直接返回原 reader")
	}
}
