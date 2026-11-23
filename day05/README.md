flowchart TD
    A[用户Query] --> B[Query预处理<br/>清洗/改写/HyDE]
    B --> C[并行多路召回]
    C --> C1[向量召回<br/>Embedding→向量库 Top50]
    C --> C2[BM25关键词召回<br/>ES/Bleve Top50]
    
    C1 & C2 --> D[前置过滤 Filter<br/>权限标签、密级、时间、文档状态]
    D --> E[去重Deduplicate<br/>key=doc_id+chunk_id，保留靠前]
    E --> F[RRF倒数排名融合<br/>k=60，合并两路排名]
    F --> G[Rerank重排 bge-reranker<br/>低于阈值丢弃]
    G --> H[取FinalTopK 6~10条Chunk]
    H --> I[拼接上下文+溯源标记]
    I --> J[LLM生成流式回答<br/>附带引用来源]