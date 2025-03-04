package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type Config struct {
	APIEndpoint      string `yaml:"api_endpoint"`      // ClashX API 地址
	APIKey           string `yaml:"api_key"`           // ClashX API 密钥
	IncludeRegex     string `yaml:"include_regex"`     // 匹配需要使用的节点正则
	ExcludeRegex     string `yaml:"exclude_regex"`     // 排除节点的正则
	TestURL          string `yaml:"test_url"`          // 测试 URL
	RetrieveInterval int    `yaml:"retrieve_interval"` // 更新所有节点的间隔时间
	CurrentInterval  int    `yaml:"current_interval"`  // 测试当前节点延迟的间隔时间
	BestInterval     int    `yaml:"best_interval"`     // 测试所有节点延迟的间隔时间，选出最优节点
	TestTimes        int    `yaml:"test_times"`        // 测试次数, 取平均值
}

type ProxyNode struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Alive bool   `json:"alive"`
	Now   string `json:"now"`
	// 可以根据 ClashX API 返回的实际字段添加更多属性
}

type ProxiesResponse struct {
	Proxies map[string]ProxyNode `json:"proxies"`
}

// 加载 YAML 配置文件
func loadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}
	return &config, nil
}

// 从 ClashX API 获取节点列表
func getProxies(apiEndpoint, apiKey string) ([]ProxyNode, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", apiEndpoint+"/proxies", nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取节点列表失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	var proxiesResp ProxiesResponse
	err = json.Unmarshal(body, &proxiesResp)
	if err != nil {
		return nil, fmt.Errorf("解析节点列表失败: %v", err)
	}
	ignoreTypes := []string{"Selector", "Direct", "URLTest", "Fallback", "LoadBalance", "Reject", "Selector"}
	var nodes []ProxyNode
	var currentSelected ProxyNode
	for _, node := range proxiesResp.Proxies {
		toIgnore := false
		for _, ignoreType := range ignoreTypes {
			if node.Type == ignoreType {
				toIgnore = true
				continue
			}
		}
		if node.Name == "🔰 节点选择" {
			currentSelected = node
		}
		if toIgnore || !node.Alive {
			continue
		}
		nodes = append(nodes, node)
	}
	if currentSelected.Name != "" {
		nodes = append(nodes, currentSelected)
	}
	return nodes, nil
}

// 根据正则表达式筛选节点
func filterNodes(nodes []ProxyNode, includeRegex, excludeRegex string) ([]ProxyNode, error) {
	includeRe, err := regexp.Compile(includeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的匹配正则表达式: %v", err)
	}
	excludeRe, err := regexp.Compile(excludeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的排除正则表达式: %v", err)
	}

	var filtered []ProxyNode
	for _, node := range nodes {
		if includeRe.MatchString(node.Name) && !excludeRe.MatchString(node.Name) {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

// 测试节点延迟，返回延迟时间（毫秒），如果不可用返回 -1
func testNode(apiEndpoint, apiKey, nodeName, testURL string) int {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/proxies/%s/delay?url=%s&timeout=5000", apiEndpoint, nodeName, testURL), nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return -1
	}

	var result struct {
		Delay int `json:"delay"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return -1
	}

	return result.Delay
}

// 切换到指定节点
func switchNode(apiEndpoint string, apiKey string, selectionNode ProxyNode) error {
	client := &http.Client{}
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/proxies/%s", apiEndpoint, selectionNode.Name), nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	payload := selectionNode.Now
	jsonPayload, _ := json.Marshal(payload)
	req.Body = io.NopCloser(bytes.NewReader(jsonPayload))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("切换节点失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("切换节点失败，状态码: %d", resp.StatusCode)
	}

	return nil
}

// 选择最快的节点
func selectFastestNode(apiEndpoint, apiKey string, nodes []ProxyNode, testURL string, testTimes int) (string, error) {
	bestNode := ""
	bestLatency := -1

	for _, node := range nodes {
		totalLatency := 0
		successCount := 0
		for i := 0; i < testTimes; i++ {
			latency := testNode(apiEndpoint, apiKey, node.Name, testURL)
			if latency > 0 {
				totalLatency += latency
				successCount++
			}
			time.Sleep(1 * time.Second) // 避免过于频繁测试
		}
		if successCount > 0 {
			avgLatency := totalLatency / successCount
			println(node.Name, avgLatency)
			if bestLatency == -1 || avgLatency < bestLatency {
				bestLatency = avgLatency
				bestNode = node.Name
			}
		}
	}

	if bestNode == "" {
		return "", fmt.Errorf("没有可用节点")
	}
	return bestNode, nil
}

func main() {
	var configFile string

	var rootCmd = &cobra.Command{
		Use:   "autoclash",
		Short: "AutoClash is a tool to automatically select the fastest ClashX node",
		Run: func(cmd *cobra.Command, args []string) {
			config, err := loadConfig(configFile)
			if err != nil {
				log.Fatalf("加载配置失败: %v", err)
			}

			var selectionNode ProxyNode
			// 获取所有节点
			nodes, err := getProxies(config.APIEndpoint, config.APIKey)
			if err != nil {
				log.Fatalf("获取节点失败: %v", err)
			}
			if len(nodes) > 0 && nodes[len(nodes)-1].Type == "Selector" {
				selectionNode = nodes[len(nodes)-1]
				nodes = nodes[:len(nodes)-1]
			} else {
				log.Fatalf("未找到节点选择器")
			}

			// 筛选节点
			filteredNodes, err := filterNodes(nodes, config.IncludeRegex, config.ExcludeRegex)
			if err != nil {
				log.Fatalf("筛选节点失败: %v", err)
			}

			if len(filteredNodes) == 0 {
				log.Fatal("没有符合条件的节点")
			}

			ticker := time.NewTicker(time.Duration(config.TestInterval) * time.Second)
			defer ticker.Stop()

			for {
				// 定时测试当前节点和所有可用节点
				if selectionNode.Now != "" {
					latency := testNode(config.APIEndpoint, config.APIKey, selectionNode.Now, config.TestURL)
					if latency == -1 {
						log.Printf("当前节点 %s 不可用，切换节点", selectionNode.Now)
						selectionNode.Now = ""
					} else {
						log.Printf("当前节点 %s 延迟 %d ms", selectionNode.Now, latency)
					}
				}

				if selectionNode.Now == "" {
					newNode, err := selectFastestNode(config.APIEndpoint, config.APIKey, filteredNodes, config.TestURL, config.TestTimes)
					if err != nil {
						log.Printf("选择最快节点失败: %v", err)
					} else {
						selectionNode.Now = newNode
						if err := switchNode(config.APIEndpoint, config.APIKey, selectionNode); err != nil {
							log.Printf("切换节点失败: %v", err)
						} else {
							log.Printf("切换到最快节点: %s", selectionNode.Now)
						}
					}
				}

				<-ticker.C
			}
		},
	}

	rootCmd.Flags().StringVarP(&configFile, "config", "C", "config.yaml", "配置文件路径")
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
