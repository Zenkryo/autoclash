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

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIEndpoint      string `yaml:"api_endpoint"`      // ClashX API 地址
	APIKey           string `yaml:"api_key"`           // ClashX API 密钥
	IncludeRegex     string `yaml:"include_regex"`     // 匹配需要使用的节点正则
	ExcludeRegex     string `yaml:"exclude_regex"`     // 排除节点的正则
	TestURL          string `yaml:"test_url"`          // 测试 URL
	RetrieveInterval int    `yaml:"retrieve_interval"` // 更新节点列表的间隔时间
	CurrentInterval  int    `yaml:"current_interval"`  // 测试当前节点的间隔时间
	BestInterval     int    `yaml:"best_interval"`     // 测试所有节点延迟的间隔时间，选出最优节点
	TestTimes        int    `yaml:"test_times"`        // 测试次数, 取平均值
	SelectNode       string `yaml:"select_node"`       // 选择节点名，默认为"🔰 节点选择"
}

type ProxyNode struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Alive bool   `json:"alive"`
	Now   string `json:"now"`
}

type ProxiesResponse struct {
	Proxies map[string]ProxyNode `json:"proxies"`
}

// 当前使用的节点名
var gNodes []string
var gCurrent string
var gConfig *Config

// 加载配置文件
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

// 从获取节点列表
func getNodes() ([]string, string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", gConfig.APIEndpoint+"/proxies", nil)
	if err != nil {
		return nil, "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+gConfig.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("获取节点列表失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("读取响应失败: %v", err)
	}

	var proxiesResp ProxiesResponse
	err = json.Unmarshal(body, &proxiesResp)
	if err != nil {
		return nil, "", fmt.Errorf("解析节点列表失败: %v", err)
	}
	ignoreTypes := []string{"Selector", "Direct", "URLTest", "Fallback", "LoadBalance", "Reject", "Selector"}
	var nodes []string
	var current string
	for _, node := range proxiesResp.Proxies {
		toIgnore := false
		for _, ignoreType := range ignoreTypes {
			if node.Type == ignoreType {
				toIgnore = true
				continue
			}
		}
		if node.Name == gConfig.SelectNode {
			current = node.Now
			continue
		}
		if toIgnore || !node.Alive {
			continue
		}
		nodes = append(nodes, node.Name)
	}
	nodes, err = filterNodes(nodes)
	if err != nil {
		return nil, "", fmt.Errorf("筛选节点失败: %v", err)
	}
	return nodes, current, nil
}

// 根据正则表达式筛选节点
func filterNodes(nodes []string) ([]string, error) {
	includeRe, err := regexp.Compile(gConfig.IncludeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的匹配正则表达式: %v", err)
	}
	excludeRe, err := regexp.Compile(gConfig.ExcludeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的排除正则表达式: %v", err)
	}

	var filtered []string
	for _, node := range nodes {
		if includeRe.MatchString(node) && !excludeRe.MatchString(node) {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

// 测试节点延迟，返回延迟时间（毫秒），如果不可用返回 -1
func testNode(nodeName string) int {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/proxies/%s/delay?url=%s&timeout=5000", gConfig.APIEndpoint, nodeName, gConfig.TestURL), nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+gConfig.APIKey)

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
func switchNode(nodenName string) error {
	client := &http.Client{}
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/proxies/%s", gConfig.APIEndpoint, gConfig.SelectNode), nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gConfig.APIKey)
	payload := nodenName
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
func selectFastestNode() (string, error) {
	bestNode := ""
	bestLatency := -1

	for _, node := range gNodes {
		totalLatency := 0
		successCount := 0
		for i := 0; i < gConfig.TestTimes; i++ {
			latency := testNode(node)
			if latency > 0 {
				totalLatency += latency
				successCount++
			}
			time.Sleep(1 * time.Second) // 避免过于频繁测试
		}
		if successCount > 0 {
			avgLatency := totalLatency / successCount
			println(node, avgLatency)
			if bestLatency == -1 || avgLatency < bestLatency {
				bestLatency = avgLatency
				bestNode = node
			}
		}
	}

	if bestNode == "" {
		return "", fmt.Errorf("没有可用节点")
	}
	return bestNode, nil
}

func main() {
	var err error
	var configFile string
	configFile = "config.yaml"
	gConfig, err = loadConfig(configFile)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	gNodes, gCurrent, err = getNodes()
	if err != nil {
		log.Fatalf("获取节点失败: %v", err)
	}
	testNode(gCurrent)
	_, err = selectFastestNode()
	if err != nil {
		log.Fatalf("选择最快节点失败: %v", err)
	}
}
