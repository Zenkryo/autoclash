package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	RetrieveInterval int    `yaml:"retrieve_interval"` // 更新节点列表的间隔时间
	CurrentInterval  int    `yaml:"current_interval"`  // 测试当前节点的间隔时间
	BestInterval     int    `yaml:"best_interval"`     // 测试所有节点延迟的间隔时间，选出最优节点
	TestTimes        int    `yaml:"test_times"`        // 测试次数, 取平均值
	SelectNode       string `yaml:"select_node"`       // 选择节点名，默认为"🔰 节点选择"
	LatencyThreshold int    `yaml:"latency_threshold"` // 迟延阈值
}

type ProxyNode struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Alive   bool    `json:"alive"`
	Now     string  `json:"now"`
	Flow    float64 `json:"-"`
	Latency int     `json:"-"`
}

type ProxiesResponse struct {
	Proxies map[string]ProxyNode `json:"proxies"`
}

var gConfig *Config
var gNodes []*ProxyNode
var gCurrent *ProxyNode
var gBest *ProxyNode
var mu sync.Mutex

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
	v := reflect.ValueOf(&config).Elem()
	t := v.Type()
	for i := range v.NumField() {
		field := t.Field(i)
		envName := "AUTOCLASH_" + strings.ToUpper(field.Name)
		envValue := os.Getenv(envName)
		if envValue != "" {
			v.Field(i).SetString(envValue)
		}
	}
	return &config, nil
}

// 获取节点流量系数
func getFlow(nodeName string) float64 {
	// 从节点名中提取流量系数， 名字中含有(d.dx)或(dx)的格式或者dx的格式, 例如1.0x, 1.5x, 2.0x或1x,2x
	re := regexp.MustCompile(`(\d+\.\d+)x|(\d+)x`)
	matches := re.FindStringSubmatch(nodeName)
	if len(matches) == 0 {
		return 1.0
	}
	if matches[1] != "" {
		flow, _ := strconv.ParseFloat(matches[1], 64)
		return flow
	}
	if matches[2] != "" {
		flow, _ := strconv.ParseFloat(matches[2], 64)
		return flow
	}
	return 1.0
}

// 从获取节点列表
func getNodes() ([]*ProxyNode, *ProxyNode, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", gConfig.APIEndpoint+"/proxies", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+gConfig.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("获取节点列表失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取响应失败: %v", err)
	}

	var proxiesResp ProxiesResponse
	err = json.Unmarshal(body, &proxiesResp)
	if err != nil {
		return nil, nil, fmt.Errorf("解析节点列表失败: %v", err)
	}
	ignoreTypes := []string{"Selector", "Direct", "URLTest", "Fallback", "LoadBalance", "Reject", "Selector"}
	var nodes []*ProxyNode
	var current *ProxyNode
	var currentName string
	for i := range proxiesResp.Proxies {
		toIgnore := false
		node := proxiesResp.Proxies[i]
		for _, ignoreType := range ignoreTypes {
			if node.Type == ignoreType {
				toIgnore = true
				continue
			}
		}
		if node.Name == gConfig.SelectNode {
			currentName = node.Now
			continue
		}
		if toIgnore || !node.Alive {
			continue
		}
		node.Flow = getFlow(node.Name)
		nodes = append(nodes, &node)
	}

	nodes, err = filterNodes(nodes)
	if err != nil {
		return nil, nil, fmt.Errorf("筛选节点失败: %v", err)
	}
	for i := range nodes {
		node := nodes[i]
		if node.Name == currentName {
			current = node
			break
		}
	}
	return nodes, current, nil
}

// 根据正则表达式筛选节点
func filterNodes(nodes []*ProxyNode) ([]*ProxyNode, error) {
	includeRe, err := regexp.Compile(gConfig.IncludeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的匹配正则表达式: %v", err)
	}
	excludeRe, err := regexp.Compile(gConfig.ExcludeRegex)
	if err != nil {
		return nil, fmt.Errorf("无效的排除正则表达式: %v", err)
	}

	var filtered []*ProxyNode
	for i := range nodes {
		node := nodes[i]
		if includeRe.MatchString(node.Name) && !excludeRe.MatchString(node.Name) {
			filtered = append(filtered, node)
		}
	}
	return filtered, nil
}

// 并行测试节点延迟
func testNode(node *ProxyNode) int {
	if node == nil {
		return -1
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/proxies/%s/delay?url=%s&timeout=5000", gConfig.APIEndpoint, node.Name, gConfig.TestURL), nil)
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
func switchNode(node *ProxyNode) error {
	if node == nil {
		return fmt.Errorf("无效的节点名")
	}
	client := &http.Client{}
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/proxies/%s", gConfig.APIEndpoint, gConfig.SelectNode), nil)
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gConfig.APIKey)
	payload := map[string]string{"name": node.Name}
	jsonPayload, _ := json.Marshal(payload)
	req.Body = io.NopCloser(bytes.NewReader(jsonPayload))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("切换节点失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 || resp.StatusCode < 200 {
		return fmt.Errorf("切换节点失败，状态码: %d", resp.StatusCode)
	}

	return nil
}

// 选择最优的节点
func selectFastestNode() (*ProxyNode, error) {
	var wg sync.WaitGroup

	for i := range gNodes {
		node := gNodes[i]
		wg.Add(1)
		go func(node *ProxyNode) {
			defer wg.Done()
			totalLatency := 0
			successCount := 0
			for range gConfig.TestTimes {
				latency := testNode(node)
				if latency > 0 {
					totalLatency += latency
					successCount++
				}
				time.Sleep(1 * time.Second) // 避免过于频繁测试
			}
			if successCount > 0 {
				node.Latency = totalLatency / successCount
			} else {
				node.Latency = -1
			}
		}(node)
	}

	wg.Wait()

	// 按流量系数分组节点
	nodeGroups := make(map[float64][]*ProxyNode)
	for i := range gNodes {
		node := gNodes[i]
		nodeGroups[node.Flow] = append(nodeGroups[node.Flow], node)
	}

	// 获取所有流量系数并排序
	var flowKeys []float64
	for flow := range nodeGroups {
		flowKeys = append(flowKeys, flow)
	}
	sort.Float64s(flowKeys)

	latencyThreshold := gConfig.LatencyThreshold
	for {
		for _, flow := range flowKeys {
			nodes := nodeGroups[flow]
			var bestNode *ProxyNode
			bestLatency := -1
			for i := range nodes {
				node := nodes[i]
				if node.Latency > 0 && node.Latency <= latencyThreshold {
					if bestLatency == -1 || node.Latency < bestLatency {
						bestLatency = node.Latency
						bestNode = node
					}
				}
			}

			if bestNode != nil {
				return bestNode, nil
			}
		}

		// 如果没有找到满足条件的节点，增加延迟阈值
		latencyThreshold += gConfig.LatencyThreshold / 10
		if latencyThreshold > gConfig.LatencyThreshold*2 {
			break
		}
	}
	return nil, fmt.Errorf("没有找到合适的节点")
}

// 定时更新节点列表
func startNodeUpdater() {
	ticker := time.NewTicker(time.Duration(gConfig.RetrieveInterval) * time.Second)
	defer ticker.Stop()
	for {
		mu.Lock()
		nodes, current, err := getNodes()
		if err != nil {
			log.Printf("更新节点列表失败: %v", err)
			mu.Unlock()
			time.Sleep(10 * time.Second)
			continue
		}
		if len(nodes) == 0 {
			log.Println("节点列表为空，立即重试")
			mu.Unlock()
			time.Sleep(10 * time.Second)
			continue
		}
		log.Println("更新节点列表成功")
		gNodes = nodes
		gCurrent = current
		mu.Unlock()
		<-ticker.C
	}
}

// 定时选择最优节点
func startBestNodeSelector() {
	ticker := time.NewTicker(time.Duration(gConfig.BestInterval) * time.Second)
	defer ticker.Stop()
	for {
		mu.Lock()
		if len(gNodes) > 0 && gBest == nil {
			log.Println("最优节点为空，立即选择最优节点")
			bestNode, err := selectFastestNode()
			if err != nil {
				log.Printf("选择最优节点失败: %v", err)
				mu.Unlock()
				time.Sleep(10 * time.Second)
				continue
			} else {
				gBest = bestNode
				log.Printf("最优节点: %s, 延迟: %d", bestNode.Name, bestNode.Latency)
			}
		}
		mu.Unlock()
		<-ticker.C
	}
}

// 定时检查当前节点是否可用
func startCurrentNodeChecker() {
	var err error
	ticker := time.NewTicker(time.Duration(gConfig.CurrentInterval) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		mu.Lock()
		delay := testNode(gCurrent)
		if delay == -1 || delay > gConfig.LatencyThreshold*2 {
			if gBest == nil || gBest == gCurrent {
				log.Printf("当前节点不可用，切换到最优节点")
				gBest, err = selectFastestNode()
				if err != nil {
					log.Printf("选择最优节点失败: %v", err)
					mu.Unlock()
					continue
				}
			}
			err = switchNode(gBest)
			if err != nil {
				log.Printf("切换节点失败: %v", err)
			} else {
				log.Printf("切换节点成功: %s", gBest.Name)
				gCurrent = gBest
			}
		}
		mu.Unlock()
	}
}

func main() {
	var configPath string

	var rootCmd = &cobra.Command{
		Use:   "autoclash",
		Short: "autoclash 是一个用于自动选择和切换 ClashX 节点的工具",
		Run: func(cmd *cobra.Command, args []string) {
			var err error
			gConfig, err = loadConfig(configPath)
			if err != nil {
				log.Fatalf("加载配置失败: %v", err)
			}

			go startNodeUpdater()
			go startBestNodeSelector()
			go startCurrentNodeChecker()

			select {} // 阻塞主协程
		},
	}

	rootCmd.Flags().StringVarP(&configPath, "config", "c", "config.yml", "配置文件路径")
	rootCmd.Execute()
}
