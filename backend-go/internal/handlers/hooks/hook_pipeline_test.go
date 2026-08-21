package hooks

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type pipelineTestHook struct {
	name     string
	stage    HookStage
	priority int
	run      func(context.Context, HookContext, HookResult) (HookResult, error)
}

func (h pipelineTestHook) Name() string     { return h.name }
func (h pipelineTestHook) Stage() HookStage { return h.stage }
func (h pipelineTestHook) Priority() int    { return h.priority }
func (h pipelineTestHook) Run(ctx context.Context, metadata HookContext, result HookResult) (HookResult, error) {
	return h.run(ctx, metadata, result)
}

func TestHookPipelineStableOrderAndResultPropagation(t *testing.T) {
	pipeline := NewPipeline()
	var order []string

	register := func(name string, priority int) {
		t.Helper()
		err := pipeline.Register(pipelineTestHook{
			name: name, stage: HookStagePreRequest, priority: priority,
			run: func(_ context.Context, metadata HookContext, result HookResult) (HookResult, error) {
				order = append(order, name)
				result.RequestBody = append(result.RequestBody, name...)
				result.Prompts = append(result.Prompts, metadata.Model+":"+name)
				return result, nil
			},
		})
		if err != nil {
			t.Fatalf("注册 Hook %q 失败: %v", name, err)
		}
	}

	register("second-a", 20)
	register("first", 10)
	register("second-b", 20)

	result, err := pipeline.RunPreRequest(context.Background(), HookContext{Model: "test-model"}, HookResult{RequestBody: []byte("body:")})
	if err != nil {
		t.Fatalf("执行管道失败: %v", err)
	}
	if want := []string{"first", "second-a", "second-b"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("执行顺序 = %v，期望 %v", order, want)
	}
	if got, want := string(result.RequestBody), "body:firstsecond-asecond-b"; got != want {
		t.Fatalf("请求体 = %q，期望 %q", got, want)
	}
	if want := []string{"test-model:first", "test-model:second-a", "test-model:second-b"}; !reflect.DeepEqual(result.Prompts, want) {
		t.Fatalf("提示词传播结果 = %v，期望 %v", result.Prompts, want)
	}
}

func TestHookPipelineStopsOnError(t *testing.T) {
	pipeline := NewPipeline()
	sentinel := errors.New("blocked")
	var ranAfterFailure bool

	hooks := []pipelineTestHook{
		{
			name: "mutate", stage: HookStageRequestTime, priority: 10,
			run: func(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
				result.RequestBody = []byte("changed")
				return result, nil
			},
		},
		{
			name: "reject", stage: HookStageRequestTime, priority: 20,
			run: func(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
				return result, sentinel
			},
		},
		{
			name: "must-not-run", stage: HookStageRequestTime, priority: 30,
			run: func(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
				ranAfterFailure = true
				return result, nil
			},
		},
	}
	for _, hook := range hooks {
		if err := pipeline.Register(hook); err != nil {
			t.Fatalf("注册 Hook 失败: %v", err)
		}
	}

	result, err := pipeline.RunRequestTime(context.Background(), HookContext{}, HookResult{RequestBody: []byte("initial")})
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误 = %v，期望包含 %v", err, sentinel)
	}
	var executionErr *HookExecutionError
	if !errors.As(err, &executionErr) || executionErr.Stage != HookStageRequestTime || executionErr.Hook != "reject" {
		t.Fatalf("执行错误缺少阶段或 Hook 信息: %#v", executionErr)
	}
	if got := string(result.RequestBody); got != "changed" {
		t.Fatalf("错误前的变更未保留: %q", got)
	}
	if ranAfterFailure {
		t.Fatal("错误后的 Hook 不应执行")
	}
}

func TestHookPipelineRegistrationValidation(t *testing.T) {
	pipeline := NewPipeline()
	valid := pipelineTestHook{name: "valid", stage: HookStagePostResponse, run: passthroughPipelineHook}
	if err := pipeline.Register(valid); err != nil {
		t.Fatalf("注册有效 Hook 失败: %v", err)
	}

	tests := []struct {
		name string
		hook Hook
	}{
		{name: "nil", hook: nil},
		{name: "empty name", hook: pipelineTestHook{stage: HookStagePreRequest, run: passthroughPipelineHook}},
		{name: "invalid stage", hook: pipelineTestHook{name: "invalid", stage: HookStage("invalid"), run: passthroughPipelineHook}},
		{name: "duplicate", hook: valid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := pipeline.Register(tt.hook); err == nil {
				t.Fatal("期望注册失败")
			}
		})
	}

	if _, err := pipeline.Run(context.Background(), HookStage("invalid"), HookContext{}, HookResult{}); err == nil {
		t.Fatal("执行无效阶段时应返回错误")
	}
	if _, err := pipeline.Run(nil, HookStagePreRequest, HookContext{}, HookResult{}); err == nil {
		t.Fatal("执行上下文为空时应返回错误")
	}
}

func TestHookPipelineExecutionUsesImmutableSnapshot(t *testing.T) {
	pipeline := NewPipeline()
	started := make(chan struct{})
	release := make(chan struct{})
	var blockFirstRun sync.Once
	var mu sync.Mutex
	var order []string

	blocking := pipelineTestHook{
		name: "blocking", stage: HookStagePostResponse,
		run: func(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
			blockFirstRun.Do(func() {
				close(started)
				<-release
			})
			mu.Lock()
			order = append(order, "blocking")
			mu.Unlock()
			return result, nil
		},
	}
	if err := pipeline.Register(blocking); err != nil {
		t.Fatalf("注册阻塞 Hook 失败: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := pipeline.RunPostResponse(context.Background(), HookContext{}, HookResult{})
		done <- err
	}()
	<-started

	late := pipelineTestHook{
		name: "late", stage: HookStagePostResponse,
		run: func(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
			mu.Lock()
			order = append(order, "late")
			mu.Unlock()
			return result, nil
		},
	}
	if err := pipeline.Register(late); err != nil {
		t.Fatalf("并发注册 Hook 失败: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("执行初始快照失败: %v", err)
	}

	mu.Lock()
	firstRunOrder := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"blocking"}; !reflect.DeepEqual(firstRunOrder, want) {
		t.Fatalf("运行中快照发生变化: %v", firstRunOrder)
	}

	if _, err := pipeline.RunPostResponse(context.Background(), HookContext{}, HookResult{}); err != nil {
		t.Fatalf("执行更新后的快照失败: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"blocking", "blocking", "late"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("更新后的执行顺序 = %v，期望 %v", order, want)
	}
}

func passthroughPipelineHook(_ context.Context, _ HookContext, result HookResult) (HookResult, error) {
	return result, nil
}
