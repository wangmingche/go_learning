package main

import (
	"context"
	"fmt"
)

/**
"""Day6｜RAG 高阶：Query 增强、HyDE、Self-Query、上下文压缩、检索失败兜底、RAG 评估指标
	1，掌握Query 增强系列方案： Query改写，扩展，HyDE(假设文档嵌入)，Self-Query(元数据过滤生成)
	2，理解什么时候用HyDE,什么时候简单改写，各自优缺点与开销
	3，上下文压缩（Context Compression):检索结果精简，冗余踢出，摘要压缩
	4，检索链路降级兜底策略：向量/BM25 单路挂了怎么降级，召回为空兜底分支
	5，RAG常用离线&在线评估指标，知道工程上怎么埋指标，怎么判断RAG效果变好还是变差
	6，把这些模块插入到Day5 的多路召回流水线中

知识点拆解：
 1. Query增强（Query Augmentation)
 原始用户query 往往很短，口语化，缺术语，缺上下文，直接Embedding 召回效果差
 常见手段：
  * Query Rewrite 改写：让大模型把短问句扩写成规范，清晰的检索问句，一问拆多问（子问题拆分）
      例：“这个设备怎么校准” → 拆成：①设备校准步骤；②校准验收标准；③校准常见故障
  * Query Expansion 同义词/术语扩展： 基于领域词典（大院标准，术语库）补充同义词，别名，编号
  * HyDE(Hypothetical Document Embeddings)假设文档嵌入

  用户Query → LLM生成一份【假想的回答文档】（假设文档）→ 对这份假想文档做Embedding，拿去向量检索
  ✅ 优势：
- 短 query、模糊 query，语义更丰满，向量召回质量显著提升
- 适合大院场景：用户问半截规范、模糊的制度问题
❌ 缺点：
- 多一次 LLM 调用，增加延迟、token 成本
- 模型幻觉：假设文档本身是错的，会带偏召回（重大风险）
✅ 工程约束：
- HyDE 只用来生成 embedding，**假想文档绝对不能塞进给用户的上下文**
- 开关可控：简单明确的编号 / 文号查询，关闭 HyDE，直接原 query 走 BM25

Self-Query Retrieval 自查询检索（大院元数据过滤神器）
让 LLM 根据用户自然语言，自动解析出过滤条件（时间、部门、密级、文档类型），生成结构化 filter 表达式，交给检索层过滤。
>
> 用户：“给我 2025 年放疗科发布的内部操作规程”
> LLM 输出结构化 filter：`{"dept":"放疗科","security_level":"内部","valid_start":1735689600}`
> 检索层拿到条件，和用户权限标签合并，执行库侧过滤
> ⚠️ 红线：LLM 输出的过滤条件**必须做白名单校验**，不能直接拼接传给向量库 / ES，防止注入、越权过滤

3. 上下文压缩 context Compresssion

Rerank 之后拿到的chunk 依然可能冗余，存在重复语义，大量无关短句；上下文压缩实在送入LLM之前做精简，不是历史会话压缩
三种工程方案：
1，抽取式压缩（轻量优先）：用小模型/规则，只保留chunk中和query相关语句，删掉无关段落
2， 摘要式压缩：长 chunk提炼摘要；开销更大，适合超长文档片段
3， 滑动窗口精简，重叠合并：相邻chunk 内容高度连续合并，减少重复文本

4，检索链路降级& 兜底策略（服务治理融合）
多路召回本身是分布式调用（向量服务，ES）任一服务超时/熔断必须兜底
 * 向量服务挂：降级只走BM25 关键词检索
 * ES/BM25 挂：降级只能走向量检索，关闭HyDE 减少延迟
 * 两路都正常，但召回后合格chunk数量=0：
   ** 兜底方案1：友好回复：知识库暂无资料，禁止编造
   ** 兜底方案2：放宽Rerank阈值，扩大召回TopN,重试一轮检索（限流保护，不能无线重试）
* 熔断联动：复用 Day2 gobreaker,向量 /ES 客户端单独熔断统计

5， RAG 评估指标（面试高频）
 分两类：离线评估(数据集测评)，在线埋点指标（生产持续观测）

 离线指标（测试集刷效果）
 * ReCall@K:相关文档有没有被召回（RAG最核心，优先保证召回）
 * Precision@K: 召回回来的里面真正相关的占比
 * NDCG： 考虑排序位置，靠前的相关文档权重更高
 * MRR：平均倒数排名，适合问答
 * 生产侧：BLEU，ROUE，LLM-as-judge(大模型裁判打分：是否忠实原文，是否幻觉)

 在线生产埋点指标（大院监控看板重点）
 * 召回空率：请求最终无合格chunk比例
 * Rerank 过滤率：多少召回 chunk 分数低于阈值被丢弃
 * 多路降级触发次数（向量降级，BM25降级）
 * 幻觉上报（用户反馈，人工抽检）
 * Token消耗：HyDE带来的额外输入token 统计

 ################################面试题#########################################

 1. HyDE 把假设文档丢给 LLM 当参考上下文 → 直接引入幻觉
2. Self-Query 输出的过滤条件不做白名单校验，被注入越权条件
3. 上下文压缩用摘要模式处理法规、设备参数文档，数值被改写
4. 只看 Precision，忽略 Recall：大院场景漏了有效文件是严重事故
5. 没有降级兜底：向量服务挂掉整个问答服务雪崩
6. Query 增强全量开启：短编号查询也走 HyDE，浪费 token、增加延迟

## 📝 Day6 配套面试题库

1. HyDE 原理，适用场景，风险点是什么？假想文档能不能送入 LLM 上下文？
2. Self-Query 是什么？在大院知识库有什么价值？安全风险怎么控制？
3. 上下文压缩有哪些方案？法规 / 涉密文档优先选哪种压缩方式？为什么？
4. 多路检索链路如何做降级？向量或者 BM25 服务不可用时怎么兜底？
5. RAG 评估 Recall@K 和 Precision@K 分别代表什么？大院场景优先哪个指标？
6. Query 改写、子问题拆分、HyDE 三者怎么选型？什么时候开什么时候关？
"""
*/

// SelfQuery 结构化Filter结构体+ 校验器
// SelfQueryFliter 自查询解析出来的过滤条件
type SelfQueryFilter struct{
	Dept string `json:"dept,omitempty"`
	SecurityLevel string `json:"security_level,omitempty"`
	ValidStartTs string `json:"valid_start_ts,omitempty"`
	ValidEndTs string `json:"valid_end_ts,omitempty"`
	DocType string `json:"doc_type,omitempty"`
	AllowTags []string `json:"-"` // 
}

// Validate 白名单校验，防止非法过滤字段注入
func (f *SelfQueryFilter) Validate() error {
	// 只允许上面定义的字段，禁止传入其他key
	// 示例：禁止过滤user、password、内部主键等敏感字段
	if f.Dept == "admin" {
		return errors.New("dept admin不允许自查询过滤")
	}
	return nil
}

// SelfQueryService 解析自然语言到filter
type SelfQueryService struct {
	llmCli LLMClient
}

func (s *SelfQueryService) ParseFilter(ctx context.Context, query string) (*SelfQueryFilter, error) {
	// 调用LLM，输出JSON SelfQueryFilter结构，然后执行Validate
	// 这里省略LLM调用
	return &SelfQueryFilter{
		Dept: "放疗科",
		SecurityLevel: "内部",
	}, nil
}

// HYDE服务器

type HyDESevice struct{
	llmCli LLMClient
	embeddingCli EmbeddingClient
}

type Compressor interface{
	// Compress 抽取式压缩，保留原文，不攥改关键信息
	Compress(cts context.Context,query string,chunks []ChunkItem)([]ChunkItem,error)
}


// GenHypotheticalEmbedding 生成假设文档，返回embedding,假想文本不想进上下文
func (h *HyDESevice) GenHypotheticalEmbedding(ctx context.Context,query string)([]float64,error){
	//step1 LLM 生成hypothetical doc
	hypoDoc,err:= h.llmCli.Completion(ctx,fmt.Sprintf("根据问题写一段简短回答，不要编造额外信息：%s",query))
	if err !=nil{
		return nil,err
	}
	// step2 hypoDoc转为embedding,用于向量检索
	emb,err := h.embeddingCli.GetEmbedding(ctx,hypoDoc)
	return emb, err
}


