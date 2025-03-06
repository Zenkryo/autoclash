# autoclash

## 项目简介

`autoclash` 是一个用于自动选择和切换 ClashX 节点的工具。它通过定期测试节点的延迟，选择最优节点并自动切换，以确保网络连接的稳定性和速度。该工具还考虑了节点的流量系数，优先选择流量消耗较低的节点。

## 功能

- 定期更新节点列表
- 定期测试所有节点的延迟
- 根据延迟和流量系数选择最优节点
- 自动切换到最优节点
- 定期检查当前节点的可用性

## 配置文件

配置文件 `config.yaml` 示例：

```yaml
api_endpoint: "http://localhost:9090"  # ClashX API 地址
api_key: "your_api_key"                # ClashX API 密钥
include_regex: "香港"                   # 匹配需要使用的节点正则
exclude_regex: "10x"                   # 排除节点的正则
test_url: "http://www.google.com"      # 测试 URL
retrieve_interval: 60                  # 更新节点列表的间隔时间（秒）
current_interval: 30                   # 测试当前节点的间隔时间（秒）
best_interval: 300                     # 测试所有节点延迟的间隔时间（秒）
test_times: 3                          # 测试次数，取平均值
select_node: "🔰 节点选择"               # 选择节点名
latency_threshold: 250                 # 延迟阈值（毫秒）
```

## 使用方法

1. 克隆项目到本地：

   ```sh
   git clone https://github.com/Zenkryo/autoclash.git
   cd autoclash
   ```

2. 创建并编辑配置文件 `config.yaml`：

   ```sh
   nano config.yaml
   ```

3. 运行程序：

   ```sh
   go run main.go
   ```

## 主要函数

- `loadConfig(filePath string) (*Config, error)`：加载配置文件。
- `getNodes() ([]*ProxyNode, *ProxyNode, error)`：获取节点列表并筛选节点。
- `testNode(node *ProxyNode) int`：测试节点延迟。
- `switchNode(node *ProxyNode) error`：切换到指定节点。
- `selectFastestNode() (*ProxyNode, error)`：选择最优节点。
- `startNodeUpdater()`：定时更新节点列表。
- `startBestNodeSelector()`：定时选择最优节点。
- `startCurrentNodeChecker()`：定时检查当前节点是否可用。

## 注意事项

- 请确保 ClashX 已经启动并正确配置 API。
- 请根据实际情况修改 `config.yaml` 中的配置项。
- 运行程序时，请确保网络连接正常。

## 许可证

此项目使用 MIT 许可证。详细信息请参阅 LICENSE 文件。
