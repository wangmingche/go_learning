
package main

/**
	技术：Gin+SSE+LLM 流式接口转发，适配Eino底层思想，
	能力点：Chat Completion,SSE 流式输出，Token统计，temperature/top_p 参数控制

	1. SSE： content-type: text/event-stream,Cache-control:no-cacha,Connection:keep-alive
	2. LLM 流式返回格式：stream=true,返回chunks, choices[0].delat.content 是增量文本
	3，注意：SSE是单项长来连接，客户端不能二次发数据，客户端主动关闭时服务端要检测 ctx.Done()


	
	chat Completion Api 开启 stream=true才能返回流式切片返回的结构时data增量，不是完成的message；
	2， SSE基于HTTP，服务端持续推送events,text/event-stream,长连接客户端可以主动close,
	3, temperature: 控制随机性，0~2， 越低越确定，适合RGA问答，越高创意性越强，top_P核采样，取累计概率前p的token候选，
	一般两者不能同时大调；
	4, Token计算：可以使用tiktoken-go 在go 侧提前算Token,用于限流，成本统计，上下文截断
	5，工程坑点：必须传递 request.Context(),客户端关闭页面时上下文取消，停止向上游拉流，避免资源泄露，每一次Flush();处理【Done】结束标记



**/


import (
	//"context"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pkoukk/tiktoken-go"
)

// ChatRequest 对外请求体
type ChatRequest struct {
	Messages []Message `json:"messages"`
	// 生成参数：用指针区分“未传”和“传了0”，未传则不下发给上游
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stream      bool     `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI兼容请求体
type openAIReq struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stream      bool      `json:"stream"`
}

// 流式chunk简化结构
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// 配置，改成你自己的endpoint
const (
	apiURL    = "https://api.deepseek.com/v1/chat/completions"
	modelName = "deepseek-flash"
)

// API Key 从环境变量读取，避免明文写进仓库
var apiKey = os.Getenv("DEEPSEEK_API_KEY")

func main() {
	r := gin.Default()
	// 流式对话接口
	r.POST("/chat/stream", chatStreamHandler)
	r.Run(":8080")
}

func chatStreamHandler(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// SSE Header 设置
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")

	// 构造向上游LLM的请求
	upstreamReq := openAIReq{
		Model:       modelName,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      true,
	}

	bodyBytes, _ := json.Marshal(upstreamReq)
	bodyReader := strings.NewReader(string(bodyBytes))

	httpReq, _ := http.NewRequestWithContext(c.Request.Context(), "POST", apiURL, bodyReader)
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		_, _ = fmt.Fprintf(c.Writer, "data: {\"error\":\"%s\"}\n\n", err.Error())
		c.Writer.Flush()
		return
	}
	defer resp.Body.Close()

	// 上游非200时不是SSE流，直接把错误透传给客户端，避免前端收到空流
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_, _ = fmt.Fprintf(c.Writer, "data: {\"error\":\"upstream %d: %s\"}\n\n", resp.StatusCode, strings.ReplaceAll(string(b), "\"", "'"))
		c.Writer.Flush()
		return
	}

	// 逐行读取流式返回（SSE chunk 格式：data: {...}\n\n）
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			_, _ = fmt.Fprintf(c.Writer, "data: {\"error\":\"read stream err\"}\n\n")
			c.Writer.Flush()
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 上游SSE行前缀是 data:
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// 结束标记
				_, _ = fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				c.Writer.Flush()
				break
			}
			// 透传给前端
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		}
	}
}

// tokens 的计算函数
func CalcToken(text string)(int,error){
	enc,err := tiktoken.GetEncoding("cl100k_base")
	if err!= nil{
		return 0,err
	}
	// 计算token数
	tokens := enc.Encode(text, nil, nil)

	return len(tokens), nil
}