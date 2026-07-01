# sterm - Simplified Kubernetes Terminal TUI

Go module: `github.com/Micost/sterm`
本地路径：`/home/rob/sterm`
GitHub：`https://github.com/Micost/sterm`

## 项目状态

轻量级 K8s TUI 管理工具，基于 `tcell/v2` + `client-go`。完整 CRUD + 日志 + Exec 骨架已就绪。

## 架构

```
main.go           入口：kubeconfig → k8s client → tui App
pkg/
├── k8s/
│   ├── client.go     typed + dynamic + discovery 客户端封装
│   ├── resource.go   Discover() 列出集群所有 GVR
│   ├── lister.go     List/Get/Delete/Update/ToYAML/Namespaces
│   ├── describe.go   Describe() 格式化资源摘要
│   └── logs.go       StreamLogs() + PodContainers()
└── tui/
    └── app.go        TUI 多页面应用（browser / list / detail / logs）
                       renderEvent{} 用于 PostEvent 驱动异步渲染
```

## 已实现功能

| 页面 | 快捷键 | 功能 |
|---|---|---|
| **Browser** | ↑↓/PgUp/PgDn/Home/End | 导航资源类型列表（common / crd / other 三栏） |
| | Enter | 进入资源实例列表 |
| | `n` | 打开命名空间选择器 |
| | ESC/Ctrl+C | 退出 |
| **Namespace** | ↑↓/PgUp/PgDn/Home/End | 导航 |
| | Enter | 选中命名空间（all 表示所有） |
| | `/` | 实时过滤 |
| | ESC | 返回 Browser |
| **List** | ↑↓/PgUp/PgDn/Home/End | 导航 |
| | Enter | 查看 YAML/Describe |
| | `/` | 实时过滤（匹配任意列） |
| | `x` | 删除（确认 y/N） |
| | `l` | Pod 日志流（auto-follow） |
| | `s` | 进入容器 Shell |
| | `n` | 快速切换命名空间（同 Browser） |
| **Detail** | ↑↓/PgUp/PgDn/Home/End | 滚动 |
| | `d` | 切换 YAML/Describe |
| | `e` | 编辑 YAML（外部 $EDITOR） |
| | `s` | 进入容器 Shell |
| **Logs** | ↑↓ | 滚动（自动停止跟随） |
| | End | 恢复 auto-follow |

## 依赖

- `github.com/gdamore/tcell/v2` — TUI 引擎
- `k8s.io/client-go` — K8s 客户端（typed + dynamic + discovery）
- `k8s.io/api` / `k8s.io/apimachinery` — K8s API 类型
- `sigs.k8s.io/yaml` — YAML 序列化

## Browser 分类

Browser 页面将资源类型分为三栏：

| 栏 | 说明 | 示例 |
|---|---|---|
| **common** | K8s 常用原生资源 | pods, deployments, services, configmaps, secrets, namespaces, nodes, ingresses, statefulsets, daemonsets, jobs, cronjobs, pvcs, pvs, serviceaccounts |
| **crd** | 非标准 API group 的资源（Operator/CRD） | prometheuses, certificates, etc. |
| **other** | 其他原生 K8s 资源 | replicasets, endpoints, events, roles, storageclasses, etc. |

## 设计决策

- **不用 tview**：tview 太重，裸 tcell 完全可控，渲染性能好
- **动态 client 优先**：List/Delete/Update 用 dynamic client 通用化，不用每种资源写 DAO
- **typed client 仅用于特殊操作**：Logs 和 Exec 用 typed client（PodInterface）
- **Describe 不是 kubectl describe**：只是从 unstructured 提取关键字段格式化输出，不依赖 kubectl
- **Edit 走外部编辑器**：Suspend TUI → $EDITOR → 读取修改 → Update API，和 kubectl edit 一样
- **Exec 走 kubectl 二进制**：最简单可靠的 TTY 处理方案
- **单 goroutine 事件循环**：`PollEvent` 为主，goroutine 通过 `PostEvent(&renderEvent{})` 触发渲染
- **Browser 三栏分类**：common / crd / other，按字母排序，`category()` 根据 API group 和资源名判定

## 未实现 / 计划中

- 端口转发
- 资源 YAML 编辑（已有）但不支持资源创建
- 多容器选择（日志/Shell 默认用第一个容器）
- 自定义列 / 自定义视图
- 主题换肤
- 更优雅的错误提示（当前静默忽略错误）

## 构建与运行

```bash
make dev      # go run .
make build    # go build -o sterm .
make clean    # rm -f sterm
```
