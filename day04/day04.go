//
// Author: wangmingche wangmingche@example.com
// Date: 2026-11-20 15:42:09
// LastEditors: wangmingche wangmingche@example.com
// LastEditTime: 2026-11-23 14:07:32
// Description:
//

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

/**
Day04| 分布式锁，幂等性，OpenTelemetry 链路追踪，指标& 日志埋点（LLM应用GO落地）
面向创业公司 Go 后端 + LLM 工程面试，可追加进学习清单.md

1， 核心概念：LLM场景视角
	1. 幂等性 idempotent：同一个请求重复提交多次，业务结果只生效一次，不会产生重复副作用
	LLM场景痛点：
	* 前端网络抖动，SSE 流式断连，用户点重试-》重试调用大模型，重复扣费，重复入库会话
	* 异步回调，消息重试，上游重试带来重复请求
	常用方案：
		1. **唯一业务幂等键（Idempotency Key）**（首选，对接 OpenAI 等 API 原生支持这个 header）
		客户端生成 `idempotency-key`，放到 Header；服务端以这个 key 记录处理状态（处理中 / 成功 / 失败）
		2. **数据库唯一索引**：会话 ID + 消息 ID 联合唯一，重复插入直接冲突报错
		3. **分布式锁辅助**：先抢锁，再执行业务；锁释放前拒绝同 key 重复进入
		4. **状态机**：会话流转状态（pending/success/fail），只有 pending 才允许执行

	2.分布式锁 Distributed Lock
		适用场景（LLM 场景）
		* 多实例定时任务：批量刷新向量库Embedding,定时清理过期会话缓存，防止多机器同时跑
		* 防止同一个会话并发发起两次LLM生成
		* 缓存后台异步更新任务互斥
	选型对比：
	1. **Redis 分布式锁**：（最长用：Redlock/简单SETNX+过期）
	  SETNX + px+ value 放客户端标识，防止别的协程误删锁
	  必须：锁续期（看门狗 watchdog）,执行任务超过锁过期时间续锁
	  库： github.com/redis/go-redis/v8,redsync 实现

	2， Etcd锁：强一致性，适合可靠性要求高，成本更高
	3. **Zookeeper 分布式锁**：支持高可用，可扩展，但配置复杂


	Redis锁三大问题：
	1， 锁过期早于业务执行-》看门狗续期
	2.主从切换丢锁-》Redlock算法（面试知道，工程上很多不强行Redlock，业务补偿兜底）
	3，释放锁必须校验持有者，不能直接DEL

	和singleflight区分：
	1， singleflight：合并相同请求，共享结果，并发都等一次返回；
	分布式锁：互斥，同一时刻只允许一个协程执行。
	场景一样：热点查询缓存击穿用singleflight，分布式锁用Redis锁
	2， 分布式锁是多实例场景下的互斥，确保在多个实例之间只有一个实例执行业务

	3, OpenTelemetry(OTel)链路追踪

	LLM服务链路： 前端---》Go服务---》RAG检索（向量库）---》LLM模型接口---》返回SSE流
	链路要追踪：整个调用链耗时，每一段耗时（检索耗时，Embedding耗时，LLM首token耗时TTFF））
	核心组件：
	* Trace: 一条完整请求链路
	* Span:链路里每一段操作（chat_stream,rag_retrieve,llm_completion,redis_get）
	* Attributes: 埋点属性，比如 llm.model=“deepseek-chat", temperature=0.2,token.prompt=120
	* Exporters: 导出到Jaeger/ZipKin /OTLP Collector

	LLM 重点指标（面试高频，创业公司很看重）
	1， 总生成token数，输入 token数
	2， 模型调用耗时 TTFT： Time to First Token 首token耗时（流式SSE核心指标）
	3， RAG 检索耗时，召回数量
	4， 各类错误：超时，熔断拒绝，限流拒绝，模型报错

	4，结构化日志+指标埋点（prometheus）
	日志：zap/zerolog,结构化JSON日志，带上trace_id,方便链路检索
	指标（Metrics）三类：
	*Counter:计数器，只增不减，比如llm_request_total{status="success|fail|timeout"}
	*Gauge:瞬间值，当前并发请求数,缓存key数量，比如llm_request_latency{status="success"}
	*Histogram:直方图，统计耗时分布（P50/p90/p99 TTFT,LLM 请求耗时）比如llm_request_latency{status="success"}

    依赖清单：
	go get github.com/go-redis/redis/v8
	go get github.com/go-redsync/redsync/v4
	go get github.com/go-redsync/redsync/v4/redis/goredis/v8
	go get go.opentelemetry.io/otel
	go get go.opentelemetry.io/otel/trace
	go get github.com/prometheus/client_golang/prometheus
	go get github.com/rs/zerolog

	1,分布式锁和singleflight 区别，分别什么时候用？
	singleflight:合并并发相同查询，共享结果，都场景，缓存击穿；
	分布式锁： 互斥执行，写任务，并发互斥

	2，redis分布式锁为什么要设置过期时间，为什么要看门狗？
	 防止进程 crash 锁一直不释放死锁，看门狗在任务没有跑完的时候自动续期，避免任务没做完锁过期。

	3， LLM流式SSE场景，链路追踪有什么难点？
    SSE 是长流，一次http 返回多个chunk;普通 https span只统计整体耗时，需要单独埋 TTFT指标，每个chunk 也可以打事件
	。trace_id 需要下发给前端日志，方便用户反馈问题排查

	4，幂等怎么设计LLM问答接口？
	前端 传idempotency-key;服务端以key存状态；成功缓存结构；重复请求直接返回缓存，不重复调用模型，不重复扣费；

	5， counter/Gauge/Histogra 分别适合埋什么指标？
	Counter: 请求总数，错误数， Gauge: 当前并发数；Histogra: 耗时分布（TTFT，p99）
**/
// 分布式锁

func main() {
	// 初始化分布式锁
	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	pool := goredis.NewPool(client)
	rs := redsync.New(pool)

	mutex := rs.NewMutex("look:llm:refresf_embedding", redsync.WithMutexOptions{
		Timeout: 5 * time.Second,
	})
	// 获取锁
	ctx := context.Background()
	if err := mutex.LockContext(ctx); err != nil {
		fmt.Println("获取锁失败:", err)
		return (err)
	}
	defer mutex.UnlockContext(ctx)
	// 执行业务
	fmt.Println("拿到锁，执行业务")
	time.Sleep(5 * time.Second)
}

// OTEl 简单Span 埋点 + Prometheus 指标示意

// tracer 取全局 TracerProvider 下的一个 Tracer 实例；"llm-service" 是插桩库/服务名，会作为 instrumentation_scope 上报
var tracer = otel.Tracer("llm-service")

// llmTTFTHist 声明一个 Prometheus 直方图指标，用于统计 TTFT（首 token 耗时）的分布，可算 P50/P90/P99
var llmTTFTHist = promauto.NewHistogram(prometheus.HistogramOpts{
	Name:    "llm_ttf_seconds",                 // 指标名：单位用 seconds 后缀是 Prometheus 命名惯例
	Help:    "LLM 首token耗时",                    // 指标说明，会出现在 /metrics 的 HELP 行里
	Buckets: []float64{0.1, 0.3, 0.5, 1, 2, 5}, // 分桶边界（秒），决定 histogram_quantile 的精度
})

// chatWithTrace 演示：一次 LLM 调用如何同时做「链路追踪 + 指标埋点」
// ctx：父上下文，传入后会从中继承 TraceID/SpanID，从而把本段挂到整条调用链上
// prompt：用户提问（本例只做占位，未真正使用）
func chatWithTrace(ctx context.Context, prompt string) error {
	// 创建子 Span：返回的 ctx 携带新 Span（必须用它继续往下调，否则链路会断）；span 用于设置属性和结束
	ctx, span := tracer.Start(ctx, "llm.completion",
		// 创建 Span 时直接带上属性：给该 Span 打标签 model=deepseek-chat，方便在 Jaeger 里筛选
		trace.WithAttributes(attribute.String("model", "deepseek-chat")),
	) // Span 创建结束

	// 延迟结束 Span：函数返回时自动 End 并上报耗时，即使中途 return/panic 也会执行
	defer span.End()

	// 模拟LLM调用耗时，获取首token耗时
	start := time.Now()                 // 记录调用开始时间，用来算 TTFT
	time.Sleep(400 * time.Millisecond)  // 用 sleep 模拟等待模型返回首个 token 的网络与推理耗时
	ttft := time.Since(start).Seconds() // 计算耗时并换算成秒（与指标名 *_seconds 的单位保持一致）

	// 把本次 TTFT 写入直方图：每次调用都会进入对应分桶，供 Prometheus 做分位数统计
	llmTTFTHist.Observe(ttft)

	// 给当前 Span 追加属性 ttft，这样链路上单次请求的具体耗时也能查到（与指标互补：指标看整体分布，Span 看单次）
	span.SetAttributes(attribute.Float64("ttft", ttft))
	// 模拟生成完成后的输出（真实场景这里是 SSE 写回前端或对结果做后处理）
	fmt.Print("生成完成")

	// 返回 nil 表示调用成功；真实实现里失败应 return err，并配合 span.RecordError/SetStatus 标记错误
	return nil
}

// 幂等 Key 简易校验逻辑

// idempotencykey 由前端传入，全局唯一

func handleChat(ctx context.Context, idempotencyKey string) (string, error) {
	// 1.查询redis是否已经处理过key
	// 存在：直接返回上次结果，不再调用LLM
	// 2.不存在，设置标记【处理中】，然后执行LLM调用
	//3. 成功后写入【成功+结果】：失败标记失败，允许重试
	return "", nil
}
