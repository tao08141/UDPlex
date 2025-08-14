# UDPlex Testing Framework

这个目录包含了UDPlex项目的完整测试套件，包括单元测试和集成测试。

## 目录结构

```
tests/
├── unit/                    # 单元测试
│   ├── main/               # 主程序测试
│   ├── packet/             # 数据包处理测试
│   ├── config/             # 配置解析测试
│   ├── auth/               # 认证功能测试
│   └── filter/             # 过滤器测试
├── integration/            # 集成测试
│   ├── udp_test.go         # UDP性能测试工具
│   ├── run_integration_tests.sh # 集成测试运行脚本
│   └── go.mod              # 集成测试Go模块
├── run_tests.sh            # 主测试运行脚本
├── go.mod                  # 测试Go模块
└── README.md               # 本文档
```

## 运行测试

### 使用Makefile（推荐）

```bash
# 运行所有测试
make test

# 仅运行单元测试
make test-unit

# 仅运行集成测试
make test-integration

# 构建并测试
make all
```

### 使用脚本

```bash
# 运行所有测试（单元测试 + 集成测试）
./tests/run_tests.sh

# 仅运行集成测试
./tests/integration/run_integration_tests.sh
```

### 手动运行

```bash
# 运行单个单元测试
cd tests/unit/packet
go test -v

# 运行集成测试
cd tests/integration
go run udp_test.go
```

## 测试类型

### 单元测试

单元测试覆盖各个组件的核心功能：

- **main_test.go**: JSON注释清理、程序构建测试
- **packet_test.go**: 数据包操作、引用计数、缓冲区管理测试
- **config_test.go**: 配置文件解析测试
- **auth_test.go**: 认证密钥生成、验证、加密测试
- **filter_test.go**: 数据包过滤和路由规则测试

### 集成测试

集成测试使用真实的UDP流量测试UDPlex的端到端功能：

#### 测试配置

- **Basic**: 基础转发测试
- **Auth Client-Server**: 认证和加密测试
- **Load Balancer**: 负载均衡测试

#### 测试指标

- **发送/接收数量**: 测试10秒内的数据包吞吐量
- **丢包率**: 计算丢包百分比（允许≤5%）
- **数据完整性**: 验证接收数据的校验和
- **吞吐量**: 每秒处理的数据包数量

#### 测试流程

1. 构建UDPlex测试二进制文件
2. 根据配置启动UDPlex进程（单进程或客户端-服务端）
3. 启动UDP接收器监听端口5201
4. 启动UDP发送器向端口5202发送数据
5. 持续发送10秒，记录统计数据
6. 验证丢包率、数据完整性等指标
7. 清理进程和资源

## 测试通过标准

### 单元测试
- 所有测试用例必须通过
- 代码覆盖率达到合理水平

### 集成测试
- 丢包率 ≤ 5%
- 数据完整性检查通过
- 至少能发送和接收数据包
- 无程序崩溃或异常退出

## CI/CD集成

测试框架已集成到GitHub Actions nightly构建流程中：

1. 设置Go环境
2. 运行所有测试（单元测试 + 集成测试）
3. 测试通过后继续构建流程
4. 测试失败时中止构建

## 故障排除

### 常见问题

1. **端口占用**: 确保端口5201和5202未被占用
2. **权限问题**: 确保脚本有执行权限
3. **Go版本**: 确保使用Go 1.24或更高版本
4. **进程清理**: 测试后会自动清理UDPlex进程

### 调试技巧

```bash
# 检查进程
ps aux | grep udplex

# 检查端口占用
netstat -tulpn | grep :520

# 查看详细测试输出
cd tests/integration
go run udp_test.go -v
```

## 贡献指南

添加新测试时请遵循：

1. 单元测试放在对应的`unit/`子目录
2. 集成测试添加到`integration/udp_test.go`
3. 更新此README文档
4. 确保新测试在CI环境中能正常运行