// Author: wangmingche wangmingche@example.com
// Date: 2026-11-19 14:15:58
// LastEditors: wangmingche wangmingche@example.com
// LastEditTime: 2026-11-19 15:21:38
// Description:
package main

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/juju/ratelimit"
	"github.com/sony/gobreaker"
)

/**
1.超时timeout
LLM接口特点： 流式输出耗时不稳定，不能无限等待。
* 区分：链接超时，杜超是，总请求超时
* 坑：context 超时优先，不要在http.Client 设置超时；流式场景下，是单数据块超时，不是整个请求一刀切
* Go 最佳实践：context.WithTimeout 贯穿整个调用链路，下游LLM一旦超时主动canncel，释放 goroutine
2. 指数退避重试 Exponential Backoff
 适用：瞬时抖动，网页闪断，429，5xx,不要重试 400/401/403 参数错误
 公式：
 delay = base *(2^retry_count)+_ jitter(必须随机抖动 jitter，防止惊群效应)
 * 普通指数退避：100ms-->200ms-->400-->800ms
 * 带抖动（推荐）：在区间内随机，避免多个客户端同时重试，打崩上游
 * 限制：最大重试次数，最大等待上限（cap）,防止无限等
 * 面试加分：重试放大流量，上游故障时盲目重试会雪崩，所以重试必须配合熔断

 3. 熔断Circuit Breaker
 	库： github.com/sony/gobreaker
 状态机3种：
 1. **Closed 关闭**：正常放行，统计失败率
2. **Open 打开**：失败阈值触发，直接快速失败，不去调用下游 LLM，保护上游
3. **Half-Open 半开**：冷却时间到，放少量探测请求；成功 → 切回 Closed；失败 → 回到 Open

核心配置参数：
* ReadToTrip: 判定打开的条件（比如连续失败N此/ 失败率》阈值）
* TimeOut: Open状态冷却多久进入半开
* MaxRequests:半开状态最多放行几个探测请求

### 4. 令牌桶限流 Token Bucket

作用：控制调用 LLM 接口的 QPS，平稳削峰，允许一定突发流量
原理：

- 桶内预先放令牌；按固定速率往桶里加令牌
- 每个请求拿到令牌才能执行；无令牌则等待 / 拒绝
对比漏桶：
- 令牌桶：**允许突发**（桶里攒的令牌可以一次性用掉），适合 LLM 场景（偶尔一波问答请求）
- 漏桶：强制匀速流出，不允许突发

Go 常用库：`github.com/juju/ratelimit` 令牌桶实现
区分：

- 客户端侧限流：控制自己程序向外调用 OpenAI/DeepSeek 接口，防止触发服务商 429
- 服务端限流：防止前端大量请求打进来压垮我们的 Go 服务
*/
// 令牌桶限流，5QPS，10ms窗口
var bucket = ratelimit.NewBucketWithRate(5,10)

// 2， 熔断配置
var cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
	Name: "deepseek-flash",
	MaxRequests: 100,
	Timeout:     3 * time.Second, // open冷却时间
	ReadyToTrip: func(counts gobreaker.Counts) bool {
		// 连续失败3次触发熔断打开
		return counts.Requests > 5 && float64(counts.TotalFailures)/float64(counts.Requests) > 0.5
	},
})

// 指数退避+抖动重试

func callWithRetry(ctx context.Context,maxRetry int) error {
	var err error
	baseDelay := 100 * time.Millisecond // 初始延迟时间
	for i:= 0; i < maxRetry; i++ {
		// 检查令牌桶是否有令牌
		bucket.Wait(1)

		_,err = cb.Execute(func()(interface{}, error) {
			// 调用下游LLM
			reqCtx,canncel :=context.WithTimeout(ctx,2*time.Second)
			defer canncel()

			return mockLLMCall(reqCtx)
		})
		if err == nil {
			return nil
		}
		// 指数退避
		delay := baseDelay * (1 << i)
		jitter := time.Duration(rand.Int63n(int64(delay / 2)))
		delay += jitter
		if delay > 1*time.Second {
			delay = 1 * time.Second
		}
		time.Sleep(delay)
	}
	return err
}


func mockLLMCall(ctx context.Context) (interface{}, error) {
	// 模拟LLM请求
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(500 * time.Millisecond):
		// return nil, fmt.Errorf("llm err")
		return "ok", nil
	}
}

func main() {
	if err := callWithRetry(context.Background(), 3); err != nil {
		log.Printf("调用失败：%v", err)
		return
	}
	log.Println("调用成功")
}



