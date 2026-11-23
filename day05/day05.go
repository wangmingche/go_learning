package main

/*
Day5 规划：**RAG 深度工程：多路召回、重排 Rerank、Hybrid Search（关键词 + 向量）、
过滤、去重、分数校准、幻觉抑制工程方案**
知识点拆解：
1，Hybrid Search 混合检索：向量 + BM25
* 向量检索（语义）：抓语义相似，同义词，转述，意向；缺点：对专用面歌词，编号，文件号，设备型号，政策文号召回弱，容易跑偏
* BM25 关键词检索（词匹配）：精确匹配术语，编号，文号；缺点：不能理解语义，同义词召回差
适合大院场景：标准，规范，设备手册，红头文件，编号类资料，必须双路召回

注意：私有化场景，BM25 实现的两种方案：
* 轻量：Bleve,Lucene 衍生库
* 重：Elasticsearch/OpenSearch ()

3, 多路召回完整流水线
 """
  用户Query --> Query预处理（清洗，拼写错误，Query改写/HyDE）-->多路并行召回
 {分支A： Embedding->向量库TopN(top50)
 分支B：BM25关键词检索 -》全文库TopN50}
 ------> 前置过滤Filter(放在召回后，融合钱，性能最优**)
  {
  元数据过滤： 用户权限标签，密级，部门，生效时间，文档状态（是否归档/禁用）
  大院强制：没有权限的文档，直接丢弃，**不能靠LLM自己判断权限**
  }
  -----》去重 Deduplicate
  同一个文档/同一个chunk,多路都召回了，合并，避免重复上下文浪费token,重复引用
  ----》分数归一  + 融合 RRF（Reciprocal Rank Fusion)
  向量分数和BM25 分数值域完全不一样，不能直接相加；RRF只利用牌面给，不依赖原始分数，工程最简单稳定
  ---》 Rerank 重排
  送入reranker模型（bge-reranker / jina-reranker)做精细相关性打分，输出最终Top-K
  ---> 上下文组装+引用标记
  带上来源ID，文档名称，页码，密级，时间戳
  ---》LLM生成回答，附带溯源引用
3， 过滤（Filter) 工程细节
过滤分两种：
1。 库侧过滤：向量库/ES 原生 Filter(Milvus,Qdrant,ES都支持，性能高，优先选)
2. 业务层后过滤：召回结果再过滤（兜底，适合复杂RBAC组合条件）
元数据字段示例
 """json
  {
  "doc_id": "doc-001",
  "title": "XX设备操作规程",
  "dept": "放疗科",
  "security_level": "内部",
  "valid_start": 1700000000,
  "valid_end": 1800000000,
  "uploader": "xxx",
  "permission_tags": ["dept_radiology","group_qa"]
}

 """
 红线：密级/权限不能交给大模型判断，比较检索链路拦截，否则合规事故

 4，去重 Deduplicate
 * 去重key: doc_id +chunk_id 最稳妥
 * 策略：保留排名靠前的一条，丢弃重复 chunk
 * 坑: 不同文档但是文本高度相似（多版本规范，重复转发文件），业务上是否保留？大院场景：区分版本号，旧版本可以直接过滤
 
 5. 分数校准 & RRF融合
 RRF 公式：(倒数排名融合公示，)
  \(\text{score} = \sum \frac{1}{k + rank}\)
  每一路，看名次；
一加 k，取倒数；
同文档，加总分；
分越高，排越前。

* k 一般取 60（经验值），rank 从 1 开始，
* 优势：不需要关心向量是余炫距离，内积，BM@%原始分，只看排序，鲁棒性极强，工业界RAG主流
* 对比：
  ** 简单加权：很难调参，换模型就失效
  ** RRF：开箱即用
* 实操参数： 向量召回top50,bm25 召回 top50, 融合后取top30 给rerank

6. Rerank 重排
* 作用：粗召回会带很多“看似相关实际无关”的噪声，reranker 做细粒度相关性判断
* 模型： bge-rerank-base/large, 私有化部署
 工程注意：
 ** rerank 是CPU/GPU 消耗点，不要rerank 上千条，粗召回控制总数
 ** 截断：rerank后选出最终top6~ 10给LLM，平衡召回质量和token成本
 ** Embedding是粗召回，Rerank 是精排，两者定位不要混淆
 
 7. 幻觉控制（链路侧工程方案，不是只靠prompt

 区分「prompt 层面抑制」和「检索链路层面抑制」，大院更喜欢链路方案

 1. 强溯源绑定：每一段chunk 带上来源标识，强制模型回答是标注引用[doc-xxx chunk-xxx]
 2, 上下文隔离：检索原文和系统提示词严格分离，增加分隔符，减少文档注入
 3，置信度截断： rerank分数低于阈值的chunk直接丢弃，不给模型
 4，禁止外推：System Prompt 约束"检索不到信息直接说无相关资料，禁止编造规范，编号，参数“
 5，多源交叉校验：同一个知识点有多份文档，比对内容一致性，冲突文档标记，提示模型
 6， 版本控制：知识库维护失效时间，过期文档过滤，避免召回废旧规范
 7，结果后校验：关键词/文号类问题，二次校验回答里的编号是否再召回文档中存在（代码校验，不是模型校验)


 ##  高频踩坑清单（面试加分项）

1. 向量和 BM25 直接相加分数：值域不统一，融合结果不可控
2. 过滤放在 rerank 之后：浪费算力，无关文档走完重排，性能差，权限泄露风险
3. 召回数量太小：多路召回基数不够，融合后没有优质候选
4. rerank 数量过大：GPU 耗时暴涨，接口超时
5. 权限只在前端控制、或者交给 LLM 判断：大院严重不合规
6. 不去重：大量重复 chunk 挤占上下文，token 浪费，回答冗余
7. 幻觉只靠 prompt 约束：检索进来本身就是错文档，prompt 拦不住

## 📝 Day5 配套面试题库

1. 什么是 Hybrid Search，向量检索和 BM25 各自优缺点，什么场景必须混合检索？
2. 讲一遍多路召回完整流程，每一步作用，过滤放在哪个环节最合适？
3. RRF 融合原理，为什么不用简单加权融合？
4. Rerank 在链路里作用，粗召回和重排分工？为什么不能直接用 rerank 全量检索？
5. 权限过滤怎么做？为什么不能让大模型判断用户能不能看这份文档？
6. 从检索链路层面，有哪些工程手段抑制幻觉？和 prompt 层面抑制有什么区别？
7. 召回 chunk 重复怎么处理？去重 key 怎么设计？


**/

// 模块拆分
type HybridRetriever struct{
	vectorCli *qdrant.Client // 向量检索客户端
	bm25Cli *elasticsearch.Client // BM25
	rerankCli *rerank.Client // 
	FilterSvc *PermissionFilte // 权限过滤服务
}

//一次性检索入参
type RetrieveReq struct{
	Query  string
	UserTags  []string // 用户权限标签，用于过滤
	TopKVector int
	TopKBm25   int
	RRFK       int
	RerankThresh float64
	FinalTopK   int
}

//召回单条结果
type ChunkItem struct {
	DocID      string
    ChunkID    string
    Text       string
    ScoreRaw   float64 // 原始分数
    Rank       int     // 当前分支排名
    SourceType string  // vector / bm25
    Meta       ChunkMeta
    RrfScore   float64
    RerankScore float64
}

// 流程函数： Query ->RetrieveParallel -> Filter ->Dedup ->RRFuse -> Rerank -> ReturnChunks

package main

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/samber/lo"
)

// ChunkItem 统一召回结果结构
type ChunkItem struct {
	DocID      string  `json:"doc_id"`
	ChunkID    string  `json:"chunk_id"`
	Text       string  `json:"text"`
	SourceType string  `json:"source_type"` // vector / bm25
	Rank       int     `json:"rank"`        // 当前通路内排名，从1开始
	RawScore   float64 `json:"raw_score"`
	Meta       ChunkMeta
	RrfScore   float64  `json:"rrf_score"`
	RerankScore float64 `json:"rerank_score"`
}

type ChunkMeta struct {
	PermissionTags []string `json:"permission_tags"`
	SecurityLevel  string   `json:"security_level"`
	ValidEndTs     int64    `json:"valid_end_ts"`
	Title          string   `json:"title"`
}

// RetrieveReq 检索入参
type RetrieveReq struct {
	Query        string
	UserTags     []string
	TopKVector   int
	TopKBm25     int
	RRFK         float64 // RRF平滑常数，一般60
	RerankThresh float64
	FinalTopK    int
}

// Retriever 检索器抽象
type Retriever interface {
	VectorSearch(ctx context.Context, query string, topN int) ([]ChunkItem, error)
	BM25Search(ctx context.Context, query string, topN int) ([]ChunkItem, error)
	Rerank(ctx context.Context, chunks []ChunkItem, query string) ([]ChunkItem, error)
}

// HybridService 混合检索服务
type HybridService struct {
	retriever Retriever
}

func NewHybridService(r Retriever) *HybridService {
	return &HybridService{retriever: r}
}

// RunPipeline 完整链路：多路召回 -> 过滤 -> 去重 -> RRF融合 -> Rerank -> 取top
func (s *HybridService) RunPipeline(ctx context.Context, req RetrieveReq) ([]ChunkItem, error) {
	// 1. 并行两路召回
	vecCh, bm25Ch := make(chan []ChunkItem), make(chan []ChunkItem)
	errCh := make(chan error, 2)

	go func() {
		res, err := s.retriever.VectorSearch(ctx, req.Query, req.TopKVector)
		if err != nil {
			errCh <- err
			vecCh <- nil
			return
		}
		vecCh <- res
	}()
	go func() {
		res, err := s.retriever.BM25Search(ctx, req.Query, req.TopKBm25)
		if err != nil {
			errCh <- err
			bm25Ch <- nil
			return
		}
		bm25Ch <- res
	}()

	vecList := <-vecCh
	bm25List := <-bm25List
	select {
	case e := <-errCh:
		return nil, e
	default:
	}

	all := append(vecList, bm25List...)

	// 2. 前置权限过滤
	filtered := filterByPermission(all, req.UserTags)

	// 3. 去重，doc+chunk唯一，保留第一次出现（排名靠前）
	dedup := deduplicate(filtered)

	// 4. RRF 倒数排名融合
	rrfList := calcRRF(dedup, req.RRFK)

	// 5. Rerank重排
	reranked, err := s.retriever.Rerank(ctx, rrfList, req.Query)
	if err != nil {
		return nil, err
	}

	// 6. 过滤低于阈值，截取FinalTopK
	var final []ChunkItem
	for _, item := range reranked {
		if item.RerankScore >= req.RerankThresh {
			final = append(final, item)
		}
	}
	if len(final) > req.FinalTopK {
		final = final[:req.FinalTopK]
	}
	return final, nil
}

// filterByPermission 权限过滤：用户标签命中meta权限标签
func filterByPermission(items []ChunkItem, userTags []string) []ChunkItem {
	return lo.Filter(items, func(item ChunkItem, _ int) bool {
		for _, t := range userTags {
			if lo.Contains(item.Meta.PermissionTags, t) {
				return true
			}
		}
		return false
	})
}

// deduplicate 去重 key: DocID+ChunkID
func deduplicate(items []ChunkItem) []ChunkItem {
	set := make(map[string]struct{})
	var out []ChunkItem
	for _, it := range items {
		key := it.DocID + "|" + it.ChunkID
		if _, ok := set[key]; !ok {
			set[key] = struct{}{}
			out = append(out, it)
		}
	}
	return out
}

// calcRRF RRF核心实现
func calcRRF(items []ChunkItem, k float64) []ChunkItem {
	// 先分组：同一个chunk收集所有通路的rank
	group := make(map[string][]int)
	keyMap := make(map[string]ChunkItem)
	for _, it := range items {
		key := it.DocID + "|" + it.ChunkID
		group[key] = append(group[key], it.Rank)
		keyMap[key] = it
	}

	var res []ChunkItem
	for key, ranks := range group {
		base := keyMap[key]
		var sum float64
		for _, r := range ranks {
			sum += 1.0 / (k + float64(r))
		}
		base.RrfScore = sum
		res = append(res, base)
	}

	// 按RRF分数降序
	sort.Slice(res, func(i, j int) bool {
		return res[i].RrfScore > res[j].RrfScore
	})
	return res
}

// MockRetriever 模拟实现，方便直接跑测试
type MockRetriever struct{}

func (m *MockRetriever) VectorSearch(_ context.Context, _ string, _ int) ([]ChunkItem, error) {
	return []ChunkItem{
		{DocID: "d1", ChunkID: "c1", Text: "规范A第1条", SourceType: "vector", Rank: 1, Meta: ChunkMeta{PermissionTags: ["dept1"]}},
		{DocID: "d2", ChunkID: "c1", Text: "规范B内容", SourceType: "vector", Rank: 2, Meta: ChunkMeta{PermissionTags: ["dept1"]}},
	}, nil
}
func (m *MockRetriever) BM25Search(_ context.Context, _ string, _ int) ([]ChunkItem, error) {
	return []ChunkItem{
		{DocID: "d1", ChunkID: "c1", Text: "规范A第1条", SourceType: "bm25", Rank: 3, Meta: ChunkMeta{PermissionTags: ["dept1"]}},
		{DocID: "d3", ChunkID: "c1", Text: "设备操作规程", SourceType: "bm25", Rank: 1, Meta: ChunkMeta{PermissionTags: ["dept1"]}},
	}, nil
}
func (m *MockRetriever) Rerank(_ context.Context, chunks []ChunkItem, _ string) ([]ChunkItem, error) {
	// mock: 直接给一个分数
	for i := range chunks {
		chunks[i].RerankScore = 0.6 + chunks[i].RrfScore*10
	}
	// 降序
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].RerankScore > chunks[j].RerankScore
	})
	return chunks, nil
}

func main() {
	svc := NewHybridService(&MockRetriever{})
	req := RetrieveReq{
		Query:        "查询规范A",
		UserTags:     []string{"dept1"},
		TopKVector:   50,
		TopKBm25:     50,
		RRFK:         60,
		RerankThresh: 0.5,
		FinalTopK:    6,
	}
	ctx := context.Background()
	results, err := svc.RunPipeline(ctx, req)
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	for _, r := range results {
		fmt.Printf("RRF:%.5f | Rerank:%.3f | Doc:%s | Text:%s\n", r.RrfScore, r.RerankScore, r.DocID, r.Text)
	}
}
