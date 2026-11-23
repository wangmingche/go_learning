package main

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/groupcache/singleflight"
)

/*
Day03 服务治理：降级，多级缓存，singleftlight, 防击穿
一， 核心概念（LLM场景视角）
1， 降级（Fallback）
熔断打开，下游LLM服务故障，流量洪峰，不直接报错，返回兜底结果，保证用户可用，
熔断： 要不要放行请求，降级，请求失败返回什么兜底？

降级分类：
1. 简单兜底降级，固定提示文案（“大模型服务器繁忙，请稍后重试”）
2. 缓存降级（优先）：返回该问题历史回答缓存，（RAG问答场景非常合适）
3.功能裁剪降级： 关闭高阶能力（关闭联网检索，关闭长思考，只做简短问答）
4. 柔性降级：且用备用模型（主模型DeepSeek挂了，自动切备用小模型）

区分面试题：
* 熔断：快速失败，拦住请求，保护下游
* 降级：失败之后兜底，保护用户体验
* 限流：拦住超额流量，保护自身服务
2. 缓存体系（LLM 多级缓存）

LLM 调用成本高（token 计费，延迟高），缓存是性价比最高优化手段

* L1：本地内存缓存（sync.Map/freecache/bigcache),毫秒级，适合热点高频 query）
* L2: 分布式缓存redis,跨实例共享，适合多副本Go 服务
* L3：向量库（PgVector/Chroma),语义缓存，相似问题命中历史答案

缓存 key 设计：`hash(messages+temperature+top_p+systemPrompt)`

>
> 注意：流式 SSE 结果也可以缓存；但是带随机参数、temperature>0.7、实时联网类问答，不建议缓存。

缓存三大问题：缓存穿透、缓存击穿、缓存雪崩（下面配合 singleflight 解决）

3. singleftlight(https://golang.org/x/sync/singleflight）【面试超级高频】
作用： 合并并发相同请求，只向LLM发起一次真实调用，返回结果给所有等待协程
解决： 缓存击穿（热点key 缓存刚好过期，一瞬间大量并发请求直接打到LLM上游）

边界坑：
* singleflight 没有超时，必须搭配context超时，防止协程永久阻塞
* 同一个key如果长时间卡住，所有请求等待一起超时
* 不要合并不同用户的私密会话问答（会话id不一样，key 不能相同）

4， 缓存三大问题总结

1. 缓存穿透：查询不存在问题，缓存，数据库，/向量库都miss, 请求直接打 LLM
	方案： 布隆过滤器，空值短期缓存
2. 缓存击穿：热点key 缓存刚好过期，瞬间大量并发请求直接打到LLM上游
	方案： singleflight;互斥锁，缓存永不过期+后台异步更新
3. 缓存雪崩：所有缓存同时过期，瞬间大量并发请求直接打到LLM上游，导致LLM服务故障
	方案： 缓存过期时间随机化抖动，分级过期，热点key 永不过期

go get golang.org/x/sync
go get github.com/allegro/bigcache/v3
go get github.com/go-redis/redis/v8


1. singleflight 和锁有什么区别？

> 
> 锁是互斥，同一时间只能一个协程执行；singleflight 是**同 key 并发等待同一个结果**，成功后全部共享，并发效率更高。

2. 什么时候不能用 singleflight？

> 
> 答案有随机性（temperature 高）、用户私有上下文、实时查询数据，结果不能复用。

3. 熔断失败后的降级策略怎么选型？

> 
> 优先历史缓存兜底；其次裁剪功能；最后静态提示文案。

4. LLM 语义缓存和普通 KV 缓存差别？

> 
> KV 缓存：精确匹配 query；语义缓存：向量相似度匹配相似问题，命中率更高，RAG 场景优先。

5. 雪崩怎么防？过期加随机抖动的原理？

> 
> 给每个 key 过期时间增加随机偏移，避免大量 key 在同一个时间点同时失效。

*/

var sfGroup singleflight.Group

// 模拟本地缓存
var localCache = make(map[string]string)

// 带singleflight+缓存+降级的LLM调用

func llmQuery(ctx context.Context, queryKey string) (string, error) {
	// 从本地缓存查询
	if v, ok := localCache[queryKey]; ok {
		return v, nil
	}

	// 2. singleflight 合并同key并发请求
	res, err, _ := sfGroup.Do(queryKey, func() (interface{}, error) {
		//这里内部嵌套：令牌同限流 -》gobreaker 熔断 -》指数重试 -》 超时
		resp,err := mockLLMCall(ctx,queryKey)
		if err != nil {
			return "", err
		}
		// 写入缓存
		localCache[queryKey] = resp
		return resp, nil
	})
	if err !=nil{
		// 降级 Fallback 兜底
		return "服务器繁忙", err
	}
	return res.(string), nil
}


func mockLLMRemoteCall(ctx context.Context, q string) (interface{}, error) {
	// 模拟慢LLM请求
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(600 * time.Millisecond):
		return fmt.Sprintf("回答:%s", q), nil
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 并发10个相同query，只会真实调用LLM一次
	for i := 0; i < 10; i++ {
		go func() {
			ans, err := llmQuery(ctx, "介绍RAG")
			fmt.Println(ans, err)
		}()
	}
	time.Sleep(2 * time.Second)
}